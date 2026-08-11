package sonos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DiscoverOptions struct {
	Timeout          time.Duration
	IncludeInvisible bool
}

var (
	ssdpDiscoverFunc           = ssdpDiscover
	scanSpeakerIPsFunc         = scanSpeakerIPs
	discoverViaTopologyFunc    = discoverViaTopology
	fetchDeviceDescriptionFunc = fetchDeviceDescription
	newClientForDiscover       = NewClient
	localIPv4AddrsFunc         = localIPv4Addrs
	isPortOpenFunc             = isPortOpen
	netInterfacesFunc          = net.Interfaces
	ifaceAddrsFunc             = func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() }
)

func Discover(ctx context.Context, opts DiscoverOptions) ([]Device, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	slog.Debug("discover: start", "timeout", timeout.String(), "includeInvisible", opts.IncludeInvisible)

	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ssdpTimeout := 1500 * time.Millisecond
	if timeout <= 2*time.Second {
		ssdpTimeout = timeout / 2
	}
	if ssdpTimeout <= 0 || ssdpTimeout > timeout {
		ssdpTimeout = timeout
	}

	ssdpCtx, cancelSSDP := context.WithTimeout(opCtx, ssdpTimeout)
	ssdpResults, err := ssdpDiscoverFunc(ssdpCtx, ssdpTimeout)
	cancelSSDP()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	slog.Debug("discover: ssdp finished", "timeout", ssdpTimeout.String(), "results", len(ssdpResults), "err", errString(err))

	// SSDP can be incomplete or unavailable on some networks. Scan the local
	// subnets as well so separate Sonos households (for example S1 and S2)
	// are all represented in the topology candidates.
	scanIPs, scanErr := scanSpeakerIPsFunc(opCtx, timeout)
	if scanErr != nil {
		slog.Debug("discover: subnet scan failed", "err", errString(scanErr))
	} else {
		slog.Debug("discover: subnet scan found speakers", "count", len(scanIPs))
	}

	candidates := append([]ssdpResult(nil), ssdpResults...)
	for _, ip := range scanIPs {
		candidates = append(candidates, ssdpResult{
			Location: fmt.Sprintf("http://%s:1400/xml/device_description.xml", ip),
		})
	}

	// Query every candidate and union the results. S1 and S2 systems expose
	// separate topology snapshots even when they share a network.
	out, topErr := discoverViaTopologyFunc(opCtx, timeout, candidates, opts.IncludeInvisible)
	if topErr == nil && len(out) > 0 {
		slog.Debug("discover: topology candidates succeeded", "devices", len(out))
		return out, nil
	}
	slog.Debug("discover: topology candidates failed", "err", errString(topErr))

	// Last resort: resolve every candidate directly. This still returns useful
	// devices when a legacy speaker does not provide a topology snapshot.
	httpClient := defaultHTTPClient(timeout)
	byIP := map[string]Device{}
	for _, r := range candidates {
		location := r.Location
		if location == "" {
			continue
		}
		if _, err := url.Parse(location); err != nil {
			continue
		}

		name, udn, ip, err := fetchDeviceDescriptionFunc(opCtx, httpClient, location)
		if err != nil {
			continue
		}
		if ip == "" {
			continue
		}
		if name == "" {
			name = ip
		}
		byIP[ip] = Device{
			IP:       ip,
			Name:     name,
			UDN:      udn,
			Location: location,
		}
	}

	return sortDevices(byIP), nil
}

func discoverViaTopologyFromIP(ctx context.Context, timeout time.Duration, ip string, includeInvisible bool) ([]Device, error) {
	c := newClientForDiscover(ip, timeout)
	top, err := c.GetTopology(ctx)
	if err != nil {
		slog.Debug("discover: GetTopology failed", "ip", ip, "err", errString(err))
		return nil, err
	}

	byIP := map[string]Device{}
	for _, g := range top.Groups {
		for _, m := range g.Members {
			if !includeInvisible && !m.IsVisible {
				continue
			}
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = m.IP
			}
			byIP[m.IP] = Device{
				IP:       m.IP,
				Name:     name,
				UDN:      m.UUID,
				Location: m.Location,
			}
		}
	}

	return sortDevices(byIP), nil
}

func discoverViaTopology(ctx context.Context, timeout time.Duration, results []ssdpResult, includeInvisible bool) ([]Device, error) {
	candidates := make([]string, 0, len(results))
	seenCandidates := map[string]struct{}{}
	for _, r := range results {
		if r.Location == "" {
			continue
		}
		ip, err := hostToIP(r.Location)
		if err != nil || ip == "" {
			continue
		}
		if _, ok := seenCandidates[ip]; ok {
			continue
		}
		seenCandidates[ip] = struct{}{}
		candidates = append(candidates, ip)
	}
	if len(candidates) == 0 {
		return nil, errors.New("no ssdp candidates")
	}

	slog.Debug("discover: querying topology candidates", "candidates", len(candidates))

	// Query multiple candidates and merge their results. Different Sonos
	// households return disjoint topology snapshots, including split S1/S2
	// systems on the same network.
	deadline := time.Now().Add(timeout)
	allByIP := map[string]Device{}
	for _, ip := range candidates {
		if time.Now().After(deadline) {
			break
		}
		c := newClientForDiscover(ip, timeout)
		top, err := c.GetTopology(ctx)
		if err != nil {
			slog.Debug("discover: topology candidate failed", "ip", ip, "err", errString(err))
			continue
		}

		byIP := map[string]Device{}
		for _, g := range top.Groups {
			for _, m := range g.Members {
				if !includeInvisible && !m.IsVisible {
					continue
				}
				name := strings.TrimSpace(m.Name)
				if name == "" {
					name = m.IP
				}
				byIP[m.IP] = Device{
					IP:       m.IP,
					Name:     name,
					UDN:      m.UUID,
					Location: m.Location,
				}
			}
		}

		for memberIP, device := range byIP {
			if _, exists := allByIP[memberIP]; !exists {
				allByIP[memberIP] = device
			}
		}
	}

	if len(allByIP) > 0 {
		slog.Debug("discover: topology candidates result", "devices", len(allByIP))
		return sortDevices(allByIP), nil
	}
	return nil, errors.New("topology discovery failed")
}

func sortDevices(byIP map[string]Device) []Device {
	out := make([]Device, 0, len(byIP))
	for _, d := range byIP {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].IP < out[j].IP
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func scanSpeakerIPs(ctx context.Context, timeout time.Duration) ([]string, error) {
	addrs, err := localIPv4AddrsFunc()
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errors.New("no local IPv4 addresses found")
	}
	slog.Debug("discover: subnet scan start", "interfaces", len(addrs))

	// Keep per-IP operations quick while collecting every validated speaker.
	httpClient := defaultHTTPClient(2 * time.Second)

	candidateIPs := make(chan string, 1024)
	found := make(chan string)

	workers := 128
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for ip := range candidateIPs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if !isPortOpenFunc(ip, 1400, 250*time.Millisecond) {
					continue
				}
				location := fmt.Sprintf("http://%s:1400/xml/device_description.xml", ip)
				_, _, _, err := fetchDeviceDescriptionFunc(ctx, httpClient, location)
				if err != nil {
					continue
				}

				select {
				case found <- ip:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(candidateIPs)
		seen := map[string]struct{}{}
		for _, ip := range addrs {
			prefix := ipTo24Prefix(ip)
			if prefix == "" {
				continue
			}
			if _, ok := seen[prefix]; ok {
				continue
			}
			seen[prefix] = struct{}{}

			for host := 1; host <= 254; host++ {
				select {
				case candidateIPs <- fmt.Sprintf("%s.%d", prefix, host):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	go func() {
		wg.Wait()
		close(found)
	}()

	foundSet := map[string]struct{}{}
	for ip := range found {
		foundSet[ip] = struct{}{}
	}
	if len(foundSet) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("no sonos speakers found on local subnets")
	}

	ips := make([]string, 0, len(foundSet))
	for ip := range foundSet {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool {
		return compareIPv4(ips[i], ips[j]) < 0
	})
	slog.Debug("discover: subnet scan found speakers", "count", len(ips))
	return ips, nil
}

func compareIPv4(a, b string) int {
	aIP := net.ParseIP(a).To4()
	bIP := net.ParseIP(b).To4()
	for i := 0; i < len(aIP) && i < len(bIP); i++ {
		if aIP[i] < bIP[i] {
			return -1
		}
		if aIP[i] > bIP[i] {
			return 1
		}
	}
	return strings.Compare(a, b)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func localIPv4Addrs() ([]net.IP, error) {
	ifaces, err := netInterfacesFunc()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, iface := range ifaces {
		// Skip down interfaces and loopback.
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifaceAddrsFunc(iface)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			ips = append(ips, ip4)
		}
	}
	return ips, nil
}

func ipTo24Prefix(ip net.IP) string {
	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", ip4[0], ip4[1], ip4[2])
}

func isPortOpen(ip string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

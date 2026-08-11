package sonos

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDiscoverFallsBackWhenSSDPDeadlineExceeded(t *testing.T) {
	origSSDP := ssdpDiscoverFunc
	origScan := scanSpeakerIPsFunc
	origTop := discoverViaTopologyFunc
	t.Cleanup(func() {
		ssdpDiscoverFunc = origSSDP
		scanSpeakerIPsFunc = origScan
		discoverViaTopologyFunc = origTop
	})

	ssdpDiscoverFunc = func(ctx context.Context, timeout time.Duration) ([]ssdpResult, error) {
		return nil, context.DeadlineExceeded
	}
	scanSpeakerIPsFunc = func(ctx context.Context, timeout time.Duration) ([]string, error) {
		return []string{"192.168.1.10"}, nil
	}
	discoverViaTopologyFunc = func(ctx context.Context, timeout time.Duration, results []ssdpResult, includeInvisible bool) ([]Device, error) {
		if len(results) != 1 || results[0].Location != "http://192.168.1.10:1400/xml/device_description.xml" {
			t.Fatalf("unexpected topology candidates: %#v", results)
		}
		return []Device{{IP: "192.168.1.10", Name: "Office", UDN: "RINCON_x"}}, nil
	}

	devs, err := Discover(context.Background(), DiscoverOptions{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(devs) != 1 || devs[0].Name != "Office" {
		t.Fatalf("unexpected devices: %#v", devs)
	}
}

func TestDiscoverUsesDefaultTimeout(t *testing.T) {
	origSSDP := ssdpDiscoverFunc
	origScan := scanSpeakerIPsFunc
	origTop := discoverViaTopologyFunc
	t.Cleanup(func() {
		ssdpDiscoverFunc = origSSDP
		scanSpeakerIPsFunc = origScan
		discoverViaTopologyFunc = origTop
	})

	ssdpDiscoverFunc = func(ctx context.Context, timeout time.Duration) ([]ssdpResult, error) {
		return []ssdpResult{{Location: "http://192.168.1.10:1400/xml/device_description.xml"}}, nil
	}
	scanSpeakerIPsFunc = func(ctx context.Context, timeout time.Duration) ([]string, error) {
		return nil, errors.New("scan unavailable")
	}
	var got time.Duration
	discoverViaTopologyFunc = func(ctx context.Context, timeout time.Duration, results []ssdpResult, includeInvisible bool) ([]Device, error) {
		got = timeout
		return []Device{{IP: "192.168.1.10", Name: "Office", UDN: "RINCON_x"}}, nil
	}

	devs, err := Discover(context.Background(), DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", got, DefaultTimeout)
	}
	if len(devs) != 1 || devs[0].Name != "Office" {
		t.Fatalf("unexpected devices: %#v", devs)
	}
}

func TestSortDevices(t *testing.T) {
	in := map[string]Device{
		"2.2.2.2": {IP: "2.2.2.2", Name: "B"},
		"1.1.1.1": {IP: "1.1.1.1", Name: "A"},
		"1.1.1.2": {IP: "1.1.1.2", Name: "A"},
	}
	out := sortDevices(in)
	if len(out) != 3 {
		t.Fatalf("len: %d", len(out))
	}
	if out[0].Name != "A" || out[0].IP != "1.1.1.1" {
		t.Fatalf("unexpected first: %+v", out[0])
	}
	if out[1].Name != "A" || out[1].IP != "1.1.1.2" {
		t.Fatalf("unexpected second: %+v", out[1])
	}
	if out[2].Name != "B" || out[2].IP != "2.2.2.2" {
		t.Fatalf("unexpected third: %+v", out[2])
	}
}

func TestIPTo24PrefixAndIsPortOpen(t *testing.T) {
	// ipTo24Prefix
	if got := ipTo24Prefix(net.ParseIP("192.168.1.10")); got != "192.168.1" {
		t.Fatalf("ipTo24Prefix: %q", got)
	}
	if got := ipTo24Prefix(net.ParseIP("::1")); got != "" {
		t.Fatalf("ipTo24Prefix v6: %q", got)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if !isPortOpen("127.0.0.1", port, 200*time.Millisecond) {
		t.Fatalf("expected port open")
	}
	_ = ln.Close()
	if isPortOpen("127.0.0.1", port, 50*time.Millisecond) {
		t.Fatalf("expected port closed after close")
	}
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steipete/sonoscli/internal/appconfig"
	"github.com/steipete/sonoscli/internal/sonos"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func topologyResponse(zgs string) *http.Response {
	return httpResponse(http.StatusOK, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetZoneGroupStateResponse xmlns:u="urn:schemas-upnp-org:service:ZoneGroupTopology:1">
      <ZoneGroupState><![CDATA[`+zgs+`]]></ZoneGroupState>
    </u:GetZoneGroupStateResponse>
  </s:Body>
</s:Envelope>`)
}

func TestExecute_Version(t *testing.T) {
	oldArgs := os.Args
	oldStdout := os.Stdout
	oldLoad := loadAppConfig
	t.Cleanup(func() {
		os.Args = oldArgs
		os.Stdout = oldStdout
		loadAppConfig = oldLoad
	})

	loadAppConfig = func() (appconfig.Config, error) {
		return appconfig.Config{DefaultRoom: "Office", Format: "plain"}, nil
	}

	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Args = []string{"sonos", "--version"}

	err := Execute()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()

	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(buf.String(), "sonos ") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

func TestResolveTargetCoordinatorIP_IPAndName(t *testing.T) {
	t.Setenv("SONOSCLI_COMPLETION_CACHE_DIR", t.TempDir())
	oldNew := newSonosClient
	oldDiscover := sonosDiscover
	t.Cleanup(func() {
		newSonosClient = oldNew
		sonosDiscover = oldDiscover
	})

	zgs := `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="RINCON_COORD1400" ID="RINCON_COORD1400:1">` +
		`<ZoneGroupMember ZoneName="Living Room" UUID="RINCON_COORD1400" Location="http://10.0.0.1:1400/xml/device_description.xml" Invisible="0" />` +
		`<ZoneGroupMember ZoneName="Office" UUID="RINCON_OFFICE1400" Location="http://10.0.0.2:1400/xml/device_description.xml" Invisible="0" />` +
		`</ZoneGroup></ZoneGroups></ZoneGroupState>`

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.Header.Get("SOAPACTION"), "ZoneGroupTopology:1#GetZoneGroupState") {
			return httpResponse(500, ""), nil
		}
		return topologyResponse(zgs), nil
	})

	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		return &sonos.Client{
			IP:   ip,
			Port: 1400,
			HTTP: &http.Client{Timeout: timeout, Transport: rt},
		}
	}
	sonosDiscover = func(ctx context.Context, opts sonos.DiscoverOptions) ([]sonos.Device, error) {
		return []sonos.Device{
			{IP: "10.0.0.1", Name: "Living Room"},
			{IP: "10.0.0.2", Name: "Office"},
		}, nil
	}

	ctx := context.Background()

	flags := &rootFlags{IP: "10.0.0.2", Timeout: time.Second}
	ip, err := resolveTargetCoordinatorIP(ctx, flags)
	if err != nil {
		t.Fatalf("resolveTargetCoordinatorIP(ip): %v", err)
	}
	if ip != "10.0.0.1" {
		t.Fatalf("expected coordinator 10.0.0.1, got %q", ip)
	}

	flags2 := &rootFlags{Name: "Office", Timeout: time.Second}
	ip2, err := resolveTargetCoordinatorIP(ctx, flags2)
	if err != nil {
		t.Fatalf("resolveTargetCoordinatorIP(name): %v", err)
	}
	if ip2 != "10.0.0.1" {
		t.Fatalf("expected coordinator 10.0.0.1, got %q", ip2)
	}

	c, err := coordinatorClient(ctx, flags2)
	if err != nil {
		t.Fatalf("coordinatorClient: %v", err)
	}
	if c.IP != "10.0.0.1" {
		t.Fatalf("unexpected client ip: %q", c.IP)
	}
}

func TestResolveTargetCoordinatorIP_UsesCachedFootholdAndFreshTopology(t *testing.T) {
	t.Setenv("SONOSCLI_COMPLETION_CACHE_DIR", t.TempDir())
	if err := storeDiscoveredDevices(time.Now(), []sonos.Device{{Name: "Office", IP: "10.0.0.2"}}); err != nil {
		t.Fatalf("store discovered devices: %v", err)
	}

	oldNew := newSonosClient
	oldDiscover := sonosDiscover
	t.Cleanup(func() {
		newSonosClient = oldNew
		sonosDiscover = oldDiscover
	})

	zgs := `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="RINCON_NEW1400" ID="RINCON_NEW1400:1">` +
		`<ZoneGroupMember ZoneName="New Coordinator" UUID="RINCON_NEW1400" Location="http://10.0.0.9:1400/xml/device_description.xml" Invisible="0" />` +
		`<ZoneGroupMember ZoneName="Office" UUID="RINCON_OFFICE1400" Location="http://10.0.0.2:1400/xml/device_description.xml" Invisible="0" />` +
		`</ZoneGroup></ZoneGroups></ZoneGroupState>`

	var clientIP string
	var clientTimeout time.Duration
	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		clientIP = ip
		clientTimeout = timeout
		return &sonos.Client{
			IP:   ip,
			Port: 1400,
			HTTP: &http.Client{Timeout: timeout, Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return topologyResponse(zgs), nil
			})},
		}
	}
	sonosDiscover = func(context.Context, sonos.DiscoverOptions) ([]sonos.Device, error) {
		t.Fatal("cache hit should skip discovery")
		return nil, nil
	}

	ip, err := resolveTargetCoordinatorIP(context.Background(), &rootFlags{Name: "office", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("resolve cached coordinator: %v", err)
	}
	if ip != "10.0.0.9" {
		t.Fatalf("coordinator = %q, want current topology coordinator", ip)
	}
	if clientIP != "10.0.0.2" {
		t.Fatalf("topology foothold = %q, want cached room IP", clientIP)
	}
	if clientTimeout != cachedTopologyProbeTimeout {
		t.Fatalf("cache probe timeout = %s, want %s", clientTimeout, cachedTopologyProbeTimeout)
	}
}

func TestResolveTargetCoordinatorIP_DeadCachedFootholdFallsBackAndRefreshes(t *testing.T) {
	t.Setenv("SONOSCLI_COMPLETION_CACHE_DIR", t.TempDir())
	if err := storeDiscoveredDevices(time.Now(), []sonos.Device{{Name: "Office", IP: "10.0.0.99"}}); err != nil {
		t.Fatalf("store discovered devices: %v", err)
	}

	oldNew := newSonosClient
	oldDiscover := sonosDiscover
	t.Cleanup(func() {
		newSonosClient = oldNew
		sonosDiscover = oldDiscover
	})

	zgs := `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="RINCON_OFFICE1400" ID="RINCON_OFFICE1400:1">` +
		`<ZoneGroupMember ZoneName="Office" UUID="RINCON_OFFICE1400" Location="http://10.0.0.2:1400/xml/device_description.xml" Invisible="0" />` +
		`</ZoneGroup></ZoneGroups></ZoneGroupState>`

	var cachedProbeTimeout time.Duration
	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
			if ip == "10.0.0.99" {
				return nil, errors.New("speaker offline")
			}
			return topologyResponse(zgs), nil
		})
		if ip == "10.0.0.99" {
			cachedProbeTimeout = timeout
		}
		return &sonos.Client{IP: ip, Port: 1400, HTTP: &http.Client{Timeout: timeout, Transport: transport}}
	}
	discoveryCalls := 0
	sonosDiscover = func(context.Context, sonos.DiscoverOptions) ([]sonos.Device, error) {
		discoveryCalls++
		return []sonos.Device{{Name: "Office", IP: "10.0.0.2"}}, nil
	}

	ip, err := resolveTargetCoordinatorIP(context.Background(), &rootFlags{Name: "Office", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("resolve after cache failure: %v", err)
	}
	if ip != "10.0.0.2" || discoveryCalls != 1 {
		t.Fatalf("fallback result = %q with %d discovery calls", ip, discoveryCalls)
	}
	if cachedProbeTimeout != cachedTopologyProbeTimeout {
		t.Fatalf("dead cache probe timeout = %s, want %s", cachedProbeTimeout, cachedTopologyProbeTimeout)
	}
	if cachedIP, ok := cachedTopologyFootholdIP(time.Now(), "Office"); !ok || cachedIP != "10.0.0.2" {
		t.Fatalf("refreshed cache = %q, %v", cachedIP, ok)
	}
}

func TestResolveTargetCoordinatorIP_UsesMatchedHouseholdFoothold(t *testing.T) {
	t.Setenv("SONOSCLI_COMPLETION_CACHE_DIR", t.TempDir())
	oldNew := newSonosClient
	oldDiscover := sonosDiscover
	t.Cleanup(func() {
		newSonosClient = oldNew
		sonosDiscover = oldDiscover
	})

	sonosDiscover = func(context.Context, sonos.DiscoverOptions) ([]sonos.Device, error) {
		return []sonos.Device{
			{Name: "Bedroom", IP: "10.0.0.10"},
			{Name: "Legacy Office", IP: "10.0.0.20"},
		}, nil
	}

	legacyTopology := `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="RINCON_LEGACY1400" ID="RINCON_LEGACY1400:1">` +
		`<ZoneGroupMember ZoneName="Legacy Office" UUID="RINCON_LEGACY1400" Location="http://10.0.0.20:1400/xml/device_description.xml" Invisible="0" />` +
		`</ZoneGroup></ZoneGroups></ZoneGroupState>`

	var foothold string
	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		foothold = ip
		return &sonos.Client{
			IP:   ip,
			Port: 1400,
			HTTP: &http.Client{Timeout: timeout, Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return topologyResponse(legacyTopology), nil
			})},
		}
	}

	ip, err := resolveTargetCoordinatorIP(context.Background(), &rootFlags{Name: "Legacy Office", Timeout: time.Second})
	if err != nil {
		t.Fatalf("resolve legacy household: %v", err)
	}
	if foothold != "10.0.0.20" {
		t.Fatalf("topology foothold = %q, want matched S1 device", foothold)
	}
	if ip != "10.0.0.20" {
		t.Fatalf("coordinator = %q, want legacy household coordinator", ip)
	}
}

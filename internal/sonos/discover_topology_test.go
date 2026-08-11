package sonos

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDiscoverViaTopologyFromIP(t *testing.T) {
	oldNewClient := newClientForDiscover
	t.Cleanup(func() { newClientForDiscover = oldNewClient })

	zgs := `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="RINCON_OFFICE1400" ID="RINCON_OFFICE1400:1">` +
		`<ZoneGroupMember ZoneName="Office" UUID="RINCON_OFFICE1400" Location="http://10.0.0.10:1400/xml/device_description.xml" Invisible="0" />` +
		`<ZoneGroupMember ZoneName="Pantry" UUID="RINCON_PANTRY1400" Location="http://10.0.0.11:1400/xml/device_description.xml" Invisible="1" />` +
		`<ZoneGroupMember ZoneName="" UUID="RINCON_EMPTY1400" Location="http://10.0.0.12:1400/xml/device_description.xml" Invisible="0" />` +
		`</ZoneGroup></ZoneGroups></ZoneGroupState>`

	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("SOAPACTION"); !strings.Contains(got, "ZoneGroupTopology:1#GetZoneGroupState") {
			t.Fatalf("SOAPACTION: %q", got)
		}
		return httpResponse(200, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetZoneGroupStateResponse xmlns:u="urn:schemas-upnp-org:service:ZoneGroupTopology:1">
      <ZoneGroupState><![CDATA[`+zgs+`]]></ZoneGroupState>
    </u:GetZoneGroupStateResponse>
  </s:Body>
</s:Envelope>`), nil
	})

	newClientForDiscover = func(ip string, timeout time.Duration) *Client {
		return &Client{
			IP:   ip,
			Port: 1400,
			HTTP: &http.Client{Timeout: timeout, Transport: rt},
		}
	}

	ctx := context.Background()

	visible, err := discoverViaTopologyFromIP(ctx, time.Second, "192.0.2.1", false)
	if err != nil {
		t.Fatalf("discoverViaTopologyFromIP: %v", err)
	}
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible devices, got %d: %#v", len(visible), visible)
	}
	if visible[0].Name != "10.0.0.12" || visible[1].Name != "Office" {
		t.Fatalf("unexpected sort/name fallback: %#v", visible)
	}

	all, err := discoverViaTopologyFromIP(ctx, time.Second, "192.0.2.1", true)
	if err != nil {
		t.Fatalf("discoverViaTopologyFromIP includeInvisible: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 devices, got %d: %#v", len(all), all)
	}
}

func TestDiscoverViaTopology_MergesSeparateHouseholds(t *testing.T) {
	oldNewClient := newClientForDiscover
	t.Cleanup(func() { newClientForDiscover = oldNewClient })

	newClientForDiscover = func(ip string, timeout time.Duration) *Client {
		var zgs string
		switch ip {
		case "10.0.0.1":
			zgs = `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="RINCON_S11400" ID="RINCON_S11400:1">` +
				`<ZoneGroupMember ZoneName="Legacy Office" UUID="RINCON_S11400" Location="http://10.0.0.1:1400/xml/device_description.xml" Invisible="0" />` +
				`</ZoneGroup></ZoneGroups></ZoneGroupState>`
		default:
			zgs = `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="RINCON_S21400" ID="RINCON_S21400:1">` +
				`<ZoneGroupMember ZoneName="Modern Office" UUID="RINCON_S21400" Location="http://10.0.0.2:1400/xml/device_description.xml" Invisible="0" />` +
				`</ZoneGroup></ZoneGroups></ZoneGroupState>`
		}

		rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("SOAPACTION"); !strings.Contains(got, "ZoneGroupTopology:1#GetZoneGroupState") {
				t.Fatalf("SOAPACTION: %q", got)
			}
			return httpResponse(200, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetZoneGroupStateResponse xmlns:u="urn:schemas-upnp-org:service:ZoneGroupTopology:1">
      <ZoneGroupState><![CDATA[`+zgs+`]]></ZoneGroupState>
    </u:GetZoneGroupStateResponse>
  </s:Body>
</s:Envelope>`), nil
		})

		return &Client{
			IP:   ip,
			Port: 1400,
			HTTP: &http.Client{Timeout: timeout, Transport: rt},
		}
	}

	results := []ssdpResult{
		{Location: "http://10.0.0.1:1400/xml/device_description.xml"},
		{Location: "http://10.0.0.2:1400/xml/device_description.xml"},
	}

	out, err := discoverViaTopology(context.Background(), 2*time.Second, results, false)
	if err != nil {
		t.Fatalf("discoverViaTopology: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 devices, got %d: %#v", len(out), out)
	}
	if out[0].Name != "Legacy Office" || out[1].Name != "Modern Office" {
		t.Fatalf("unexpected devices: %#v", out)
	}
}

func TestDiscoverViaTopology_NoCandidates(t *testing.T) {
	if _, err := discoverViaTopology(context.Background(), time.Second, nil, false); err == nil {
		t.Fatalf("expected error")
	}
}

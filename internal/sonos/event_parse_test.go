package sonos

import (
	"testing"
	"time"
)

func TestParseSecondTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		header       string
		wantTimeout  time.Duration
		wantInfinite bool
		wantErr      bool
	}{
		{name: "finite", header: "Second-1800", wantTimeout: 30 * time.Minute},
		{name: "finite whitespace", header: "  second-60  ", wantTimeout: time.Minute},
		{name: "infinite", header: " Infinite ", wantInfinite: true},
		{name: "missing", wantErr: true},
		{name: "zero", header: "Second-0", wantErr: true},
		{name: "negative", header: "Second--1", wantErr: true},
		{name: "malformed", header: "Second-nope", wantErr: true},
		{name: "overflow", header: "Second-9223372037", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotTimeout, gotInfinite, err := parseSecondTimeout(tt.header)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSecondTimeout(%q) error = %v, wantErr %v", tt.header, err, tt.wantErr)
			}
			if gotTimeout != tt.wantTimeout || gotInfinite != tt.wantInfinite {
				t.Fatalf("parseSecondTimeout(%q) = (%s, %v), want (%s, %v)", tt.header, gotTimeout, gotInfinite, tt.wantTimeout, tt.wantInfinite)
			}
		})
	}
}

func TestParseEventLastChangeFlatten(t *testing.T) {
	payload := []byte(`<?xml version="1.0"?>
<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0">
  <e:property>
    <LastChange>&lt;Event xmlns=&quot;urn:schemas-upnp-org:metadata-1-0/AVT/&quot;&gt;
      &lt;InstanceID val=&quot;0&quot;&gt;
        &lt;TransportState val=&quot;PLAYING&quot;/&gt;
        &lt;CurrentTrackURI val=&quot;x-sonos-spotify:spotify%3atrack%3aabc&quot;/&gt;
      &lt;/InstanceID&gt;
    &lt;/Event&gt;</LastChange>
  </e:property>
</e:propertyset>`)

	vars, err := ParseEvent(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["transport_state"] != "PLAYING" {
		t.Fatalf("transport_state=%q", vars["transport_state"])
	}
	if vars["current_track_uri"] == "" {
		t.Fatalf("missing current_track_uri")
	}
}

func TestParseEventChannelFlatten(t *testing.T) {
	payload := []byte(`<?xml version="1.0"?>
<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0">
  <e:property>
    <LastChange>&lt;Event xmlns=&quot;urn:schemas-upnp-org:metadata-1-0/RCS/&quot;&gt;
      &lt;InstanceID val=&quot;0&quot;&gt;
        &lt;Volume channel=&quot;Master&quot; val=&quot;12&quot;/&gt;
        &lt;Mute channel=&quot;Master&quot; val=&quot;0&quot;/&gt;
      &lt;/InstanceID&gt;
    &lt;/Event&gt;</LastChange>
  </e:property>
</e:propertyset>`)

	vars, err := ParseEvent(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["volume_master"] != "12" {
		t.Fatalf("volume_master=%q", vars["volume_master"])
	}
	if vars["mute_master"] != "0" {
		t.Fatalf("mute_master=%q", vars["mute_master"])
	}
}

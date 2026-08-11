package sonos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestSubscribeRenewUnsubscribe(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	handle := func(expectedSID string, initialTimeout string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case "SUBSCRIBE":
				if r.Header.Get("SID") != "" {
					// renew
					w.Header().Set("TIMEOUT", "Second-120")
					w.WriteHeader(http.StatusOK)
					return
				}
				// initial subscribe
				if r.Header.Get("NT") != "upnp:event" {
					t.Fatalf("NT: %q", r.Header.Get("NT"))
				}
				if r.Header.Get("CALLBACK") == "" {
					t.Fatalf("missing CALLBACK")
				}
				w.Header().Set("SID", expectedSID)
				w.Header().Set("TIMEOUT", initialTimeout)
				w.WriteHeader(http.StatusOK)
			case "UNSUBSCRIBE":
				if r.Header.Get("SID") != expectedSID {
					t.Fatalf("SID: %q", r.Header.Get("SID"))
				}
				w.WriteHeader(http.StatusPreconditionFailed)
			default:
				t.Fatalf("method: %s", r.Method)
			}
		}
	}

	mux.HandleFunc(eventAVTransport, handle("uuid:sub-1", "Second-60"))
	mux.HandleFunc(eventRenderingControl, handle("uuid:sub-rc", "infinite"))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	c := &Client{
		IP:   u.Hostname(),
		Port: port,
		HTTP: srv.Client(),
	}

	sub, err := c.SubscribeAVTransport(context.Background(), "http://127.0.0.1:12345/notify", 10*time.Second)
	if err != nil {
		t.Fatalf("SubscribeAVTransport: %v", err)
	}
	if sub.SID != "uuid:sub-1" || sub.Timeout != 60*time.Second || sub.Infinite {
		t.Fatalf("unexpected subscription: %+v", sub)
	}

	sub2, err := c.Renew(context.Background(), sub, 30*time.Second)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if sub2.Timeout != 120*time.Second || sub2.Infinite {
		t.Fatalf("renew timeout: %s", sub2.Timeout)
	}

	// 412 should be treated as success.
	if err := c.Unsubscribe(context.Background(), sub2); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	subRC, err := c.SubscribeRenderingControl(context.Background(), "http://127.0.0.1:12345/notify", 0)
	if err != nil {
		t.Fatalf("SubscribeRenderingControl: %v", err)
	}
	if subRC.SID != "uuid:sub-rc" || subRC.Timeout != 0 || !subRC.Infinite {
		t.Fatalf("unexpected rc subscription: %+v", subRC)
	}
	if err := c.Unsubscribe(context.Background(), subRC); err != nil {
		t.Fatalf("Unsubscribe(rc): %v", err)
	}
}

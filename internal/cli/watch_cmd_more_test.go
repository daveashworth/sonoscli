package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steipete/sonoscli/internal/sonos"
)

func TestWatchCmd_TSVAndMethodNotAllowed(t *testing.T) {
	callbackCh := make(chan string, 1)
	subscribedCh := make(chan struct{}, 1)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ZoneGroupTopology/Control":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case r.Method == "SUBSCRIBE" && r.URL.Path == "/MediaRenderer/AVTransport/Event":
			cb := strings.Trim(strings.TrimSpace(r.Header.Get("CALLBACK")), "<>")
			w.Header().Set("SID", "uuid:avt")
			w.Header().Set("TIMEOUT", "Second-1800")
			w.WriteHeader(http.StatusOK)
			select {
			case callbackCh <- cb:
			default:
			}
			return
		case r.Method == "SUBSCRIBE" && r.URL.Path == "/MediaRenderer/RenderingControl/Event":
			w.Header().Set("SID", "uuid:rc")
			w.Header().Set("TIMEOUT", "Second-1800")
			w.WriteHeader(http.StatusOK)
			select {
			case subscribedCh <- struct{}{}:
			default:
			}
			return
		case r.Method == "UNSUBSCRIBE":
			w.WriteHeader(http.StatusOK)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	oldNew := newSonosClient
	t.Cleanup(func() { newSonosClient = oldNew })
	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		return &sonos.Client{IP: u.Hostname(), Port: port, HTTP: srv.Client()}
	}

	flags := &rootFlags{IP: u.Hostname(), Timeout: 2 * time.Second, Format: formatTSV}
	cmd := newWatchCmd(flags)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--duration", "200ms"})

	var out syncBuffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(context.Background()) }()

	var callbackURL string
	select {
	case callbackURL = <-callbackCh:
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for callback")
	}
	select {
	case <-subscribedCh:
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for subscriptions")
	}

	// Ensure non-NOTIFY methods are rejected.
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}

	// Emit a NOTIFY event.
	ev := `<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0"><e:property>` +
		`<LastChange>` +
		`&lt;Event xmlns=&quot;urn:schemas-upnp-org:metadata-1-0/AVT/&quot;&gt;` +
		`&lt;InstanceID val=&quot;0&quot;&gt;` +
		`&lt;TransportState val=&quot;PLAYING&quot;/&gt;` +
		`&lt;/InstanceID&gt;` +
		`&lt;/Event&gt;` +
		`</LastChange>` +
		`</e:property></e:propertyset>`

	req, _ := http.NewRequest("NOTIFY", callbackURL, strings.NewReader(ev))
	req.Header.Set("SID", "uuid:avt")
	req.Header.Set("SEQ", "1")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	_, _ = io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("watch did not exit")
	}

	got := out.String()
	fields := strings.Split(strings.TrimSpace(got), "\t")
	if len(fields) != 5 {
		t.Fatalf("unexpected tsv output: %q", got)
	}
	if _, err := time.Parse(time.RFC3339Nano, fields[0]); err != nil {
		t.Fatalf("unexpected tsv timestamp %q: %v", fields[0], err)
	}
	want := []string{"avtransport", "uuid:avt", "transport_state", "PLAYING"}
	for i, field := range fields[1:] {
		if field != want[i] {
			t.Fatalf("unexpected tsv output: %q", got)
		}
	}
}

func TestWatchCmd_PlainParseError(t *testing.T) {
	callbackCh := make(chan string, 1)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ZoneGroupTopology/Control":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case r.Method == "SUBSCRIBE" && r.URL.Path == "/MediaRenderer/AVTransport/Event":
			cb := strings.Trim(strings.TrimSpace(r.Header.Get("CALLBACK")), "<>")
			w.Header().Set("SID", "uuid:avt")
			w.Header().Set("TIMEOUT", "Second-1800")
			w.WriteHeader(http.StatusOK)
			select {
			case callbackCh <- cb:
			default:
			}
			return
		case r.Method == "SUBSCRIBE" && r.URL.Path == "/MediaRenderer/RenderingControl/Event":
			w.Header().Set("SID", "uuid:rc")
			w.Header().Set("TIMEOUT", "Second-1800")
			w.WriteHeader(http.StatusOK)
			return
		case r.Method == "UNSUBSCRIBE":
			w.WriteHeader(http.StatusOK)
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	oldNew := newSonosClient
	t.Cleanup(func() { newSonosClient = oldNew })
	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		return &sonos.Client{IP: u.Hostname(), Port: port, HTTP: srv.Client()}
	}

	flags := &rootFlags{IP: u.Hostname(), Timeout: 2 * time.Second, Format: formatPlain}
	cmd := newWatchCmd(flags)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--duration", "200ms"})

	var out syncBuffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(context.Background()) }()

	var callbackURL string
	select {
	case callbackURL = <-callbackCh:
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for callback")
	}

	// Send malformed XML body to trigger parse_error.
	req, _ := http.NewRequest("NOTIFY", callbackURL, strings.NewReader("<e:propertyset"))
	req.Header.Set("SID", "uuid:avt")
	req.Header.Set("SEQ", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("watch did not exit")
	}

	got := out.String()
	if !strings.Contains(got, "[avtransport]") || !strings.Contains(got, "parse_error=") {
		t.Fatalf("unexpected output: %q", got)
	}
}

type watchRenewResult struct {
	sub sonos.Subscription
	err error
}

type scriptedWatchRenewer struct {
	mu          sync.Mutex
	results     []watchRenewResult
	fallbackErr error
	calls       []sonos.Subscription
}

func (r *scriptedWatchRenewer) Renew(_ context.Context, sub sonos.Subscription, _ time.Duration) (sonos.Subscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sub)
	if len(r.results) == 0 {
		return sonos.Subscription{}, r.fallbackErr
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result.sub, result.err
}

func (r *scriptedWatchRenewer) calledWithTimeouts() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	timeouts := make([]time.Duration, len(r.calls))
	for i, call := range r.calls {
		timeouts[i] = call.Timeout
	}
	return timeouts
}

func TestRenewWatchSubscriptionRetriesAndUsesUpdatedLease(t *testing.T) {
	transientErr := errors.New("temporary network failure")
	renewer := &scriptedWatchRenewer{results: []watchRenewResult{
		{err: transientErr},
		{sub: sonos.Subscription{SID: "uuid:avt", Timeout: 20 * time.Millisecond}},
		{sub: sonos.Subscription{SID: "uuid:avt", Infinite: true}},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := renewWatchSubscription(ctx, renewer, "avtransport", sonos.Subscription{SID: "uuid:avt", Timeout: 10 * time.Millisecond}); err != nil {
		t.Fatalf("renewWatchSubscription: %v", err)
	}

	want := []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	got := renewer.calledWithTimeouts()
	if len(got) != len(want) {
		t.Fatalf("renew calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("renew call timeouts = %v, want %v", got, want)
		}
	}
}

func TestRenewWatchSubscriptionReportsFailureBeforeExpiry(t *testing.T) {
	renewErr := errors.New("speaker unavailable")
	renewer := &scriptedWatchRenewer{fallbackErr: renewErr}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := renewWatchSubscription(ctx, renewer, "renderingcontrol", sonos.Subscription{SID: "uuid:rc", Timeout: 100 * time.Millisecond})
	if !errors.Is(err, renewErr) {
		t.Fatalf("renewWatchSubscription error = %v, want wrapped %v", err, renewErr)
	}
	if !strings.Contains(err.Error(), "renderingcontrol") {
		t.Fatalf("renewWatchSubscription error lacks service context: %v", err)
	}
	if calls := len(renewer.calledWithTimeouts()); calls < 2 {
		t.Fatalf("renew calls = %d, want at least one retry", calls)
	}
}

func TestRenewWatchSubscriptionSkipsInfiniteLease(t *testing.T) {
	renewer := &scriptedWatchRenewer{fallbackErr: errors.New("unexpected renewal")}
	if err := renewWatchSubscription(context.Background(), renewer, "avtransport", sonos.Subscription{Infinite: true}); err != nil {
		t.Fatalf("renewWatchSubscription: %v", err)
	}
	if calls := len(renewer.calledWithTimeouts()); calls != 0 {
		t.Fatalf("renew calls = %d, want 0", calls)
	}
}

func TestWatchCmdRenewsBothSubscriptions(t *testing.T) {
	var mu sync.Mutex
	renewals := map[string]int{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ZoneGroupTopology/Control":
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == "SUBSCRIBE":
			var sid string
			switch r.URL.Path {
			case "/MediaRenderer/AVTransport/Event":
				sid = "uuid:avt"
			case "/MediaRenderer/RenderingControl/Event":
				sid = "uuid:rc"
			default:
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if gotSID := r.Header.Get("SID"); gotSID != "" {
				if gotSID != sid {
					t.Errorf("renew SID = %q, want %q", gotSID, sid)
				}
				mu.Lock()
				renewals[sid]++
				mu.Unlock()
				w.Header().Set("TIMEOUT", "infinite")
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("SID", sid)
			w.Header().Set("TIMEOUT", "Second-1")
			w.WriteHeader(http.StatusOK)
		case r.Method == "UNSUBSCRIBE":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	oldNew := newSonosClient
	t.Cleanup(func() { newSonosClient = oldNew })
	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		return &sonos.Client{IP: u.Hostname(), Port: port, HTTP: srv.Client()}
	}

	flags := &rootFlags{IP: u.Hostname(), Timeout: 2 * time.Second, Format: formatJSON}
	cmd := newWatchCmd(flags)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--duration", "1100ms"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("watch: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if renewals["uuid:avt"] != 1 || renewals["uuid:rc"] != 1 {
		t.Fatalf("renewals = %v, want one for each subscription", renewals)
	}
}

type failingWatchListener struct {
	err error
}

func (l *failingWatchListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *failingWatchListener) Close() error              { return nil }
func (l *failingWatchListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
}

func TestWatchCmdReportsCallbackServerFailure(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ZoneGroupTopology/Control":
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == "SUBSCRIBE":
			if strings.Contains(r.URL.Path, "AVTransport") {
				w.Header().Set("SID", "uuid:avt")
			} else {
				w.Header().Set("SID", "uuid:rc")
			}
			w.Header().Set("TIMEOUT", "infinite")
			w.WriteHeader(http.StatusOK)
		case r.Method == "UNSUBSCRIBE":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	oldNew := newSonosClient
	t.Cleanup(func() { newSonosClient = oldNew })
	newSonosClient = func(ip string, timeout time.Duration) *sonos.Client {
		return &sonos.Client{IP: u.Hostname(), Port: port, HTTP: srv.Client()}
	}

	listenErr := errors.New("listener failed")
	oldListen := watchListen
	t.Cleanup(func() { watchListen = oldListen })
	watchListen = func(string, string) (net.Listener, error) {
		return &failingWatchListener{err: listenErr}, nil
	}

	flags := &rootFlags{IP: u.Hostname(), Timeout: 2 * time.Second, Format: formatJSON}
	cmd := newWatchCmd(flags)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "watch callback server") || !errors.Is(err, listenErr) {
		t.Fatalf("watch error = %v, want wrapped callback server error", err)
	}
}

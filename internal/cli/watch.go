package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/sonoscli/internal/sonos"
)

type watchEvent struct {
	Time    time.Time         `json:"time"`
	Service string            `json:"service"`
	SID     string            `json:"sid"`
	Seq     string            `json:"seq"`
	Vars    map[string]string `json:"vars"`
}

const maxWatchRenewRetryDelay = 30 * time.Second

type watchSubscriptionRenewer interface {
	Renew(ctx context.Context, sub sonos.Subscription, requestedTimeout time.Duration) (sonos.Subscription, error)
}

var watchListen = net.Listen

func waitForWatchRenewal(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func watchRenewDelay(timeout time.Duration) time.Duration {
	return timeout - timeout/5
}

func watchRenewRetryDelay(timeout time.Duration) time.Duration {
	delay := timeout / 20
	if delay <= 0 {
		delay = time.Nanosecond
	}
	if delay > maxWatchRenewRetryDelay {
		return maxWatchRenewRetryDelay
	}
	return delay
}

func renewWatchSubscription(ctx context.Context, renewer watchSubscriptionRenewer, service string, sub sonos.Subscription) error {
	for {
		if sub.Infinite {
			return nil
		}
		if sub.Timeout <= 0 {
			return fmt.Errorf("%s subscription has no valid lease duration", service)
		}

		leaseDeadline := time.Now().Add(sub.Timeout)
		if !waitForWatchRenewal(ctx, watchRenewDelay(sub.Timeout)) {
			return ctx.Err()
		}

		for {
			if !time.Now().Before(leaseDeadline) {
				return fmt.Errorf("%s subscription lease expired before renewal", service)
			}

			renewed, err := renewer.Renew(ctx, sub, 0)
			if err == nil {
				sub = renewed
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}

			retryDelay := watchRenewRetryDelay(sub.Timeout)
			if time.Until(leaseDeadline) <= retryDelay {
				return fmt.Errorf("renew %s subscription before lease expiry: %w", service, err)
			}
			if !waitForWatchRenewal(ctx, retryDelay) {
				return ctx.Err()
			}
		}
	}
}

func listenIPForRemote(remoteIP string) (string, error) {
	conn, err := net.Dial("udp", net.JoinHostPort(remoteIP, "1900"))
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || udpAddr.IP == nil {
		return "", errors.New("could not determine local listen ip")
	}
	return udpAddr.IP.String(), nil
}

func newWatchCmd(flags *rootFlags) *cobra.Command {
	var duration time.Duration

	cmd := &cobra.Command{
		Use:          "watch",
		Short:        "Watch live Sonos events",
		Long:         "Subscribes to AVTransport and RenderingControl events and prints changes as they arrive (Ctrl+C to stop). Requires that Sonos speakers can reach your machine on the chosen callback port (firewall may prompt).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTarget(flags); err != nil {
				return err
			}

			ctx := cmd.Context()
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()
			if duration > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, duration)
				defer cancel()
			}

			c, err := coordinatorClient(ctx, flags)
			if err != nil {
				return err
			}

			listenIP, err := listenIPForRemote(c.IP)
			if err != nil {
				return err
			}
			ln, err := watchListen("tcp", net.JoinHostPort(listenIP, "0"))
			if err != nil {
				return err
			}
			defer func() { _ = ln.Close() }()
			port := ln.Addr().(*net.TCPAddr).Port

			callbackURL := fmt.Sprintf("http://%s:%d/notify", listenIP, port)

			events := make(chan watchEvent, 128)
			var sidToService sync.Map // sid -> service name

			mux := http.NewServeMux()
			mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "NOTIFY" {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				sid := strings.TrimSpace(r.Header.Get("SID"))
				seq := strings.TrimSpace(r.Header.Get("SEQ"))
				body, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()

				service := "unknown"
				if v, ok := sidToService.Load(sid); ok {
					service = v.(string)
				}

				vars, err := sonos.ParseEvent(body)
				if err != nil {
					vars = map[string]string{"parse_error": err.Error()}
				}

				select {
				case events <- watchEvent{
					Time:    time.Now().UTC(),
					Service: service,
					SID:     sid,
					Seq:     seq,
					Vars:    vars,
				}:
				default:
					// Drop if the consumer is too slow.
				}

				w.WriteHeader(http.StatusOK)
			})

			srv := &http.Server{
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			serverErrs := make(chan error, 1)
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					serverErrs <- fmt.Errorf("watch callback server: %w", err)
				}
			}()
			defer func() { _ = srv.Shutdown(context.Background()) }()

			avtSub, err := c.SubscribeAVTransport(ctx, callbackURL, 0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Unsubscribe(context.Background(), avtSub) }()
			sidToService.Store(avtSub.SID, "avtransport")

			rcSub, err := c.SubscribeRenderingControl(ctx, callbackURL, 0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Unsubscribe(context.Background(), rcSub) }()
			sidToService.Store(rcSub.SID, "renderingcontrol")

			renewalCtx, cancelRenewals := context.WithCancel(ctx)
			renewalErrs := make(chan error, 2)
			var renewalWG sync.WaitGroup
			startRenewal := func(service string, sub sonos.Subscription) {
				renewalWG.Add(1)
				go func() {
					defer renewalWG.Done()
					err := renewWatchSubscription(renewalCtx, c, service, sub)
					if err == nil || renewalCtx.Err() != nil {
						return
					}
					select {
					case renewalErrs <- err:
					case <-renewalCtx.Done():
					}
				}()
			}
			startRenewal("avtransport", avtSub)
			startRenewal("renderingcontrol", rcSub)
			defer func() {
				cancelRenewals()
				renewalWG.Wait()
			}()

			if !isJSON(flags) && !isTSV(flags) {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Watching events (callback %s). Press Ctrl+C to stop.\n", callbackURL)
			}

			for {
				select {
				case <-ctx.Done():
					return nil
				case err := <-serverErrs:
					return err
				case err := <-renewalErrs:
					return err
				case ev := <-events:
					if isJSON(flags) {
						_ = writeJSONLine(cmd, ev)
						continue
					}
					if isTSV(flags) {
						keys := make([]string, 0, len(ev.Vars))
						for k := range ev.Vars {
							keys = append(keys, k)
						}
						sort.Strings(keys)
						for _, k := range keys {
							_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", ev.Time.Format(time.RFC3339Nano), ev.Service, ev.SID, k, ev.Vars[k])
						}
						continue
					}

					keys := make([]string, 0, len(ev.Vars))
					for k := range ev.Vars {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					parts := make([]string, 0, len(keys))
					for _, k := range keys {
						parts = append(parts, fmt.Sprintf("%s=%s", k, ev.Vars[k]))
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s [%s] %s\n", ev.Time.Format(time.RFC3339), ev.Service, strings.Join(parts, " "))
				}
			}
		},
	}

	cmd.Flags().DurationVar(&duration, "duration", 0, "Stop after this duration (0 = until Ctrl+C)")
	return cmd
}

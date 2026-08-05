package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type fakeServer struct {
	events      *[]string
	done        chan struct{}
	shutdownErr error
}

func (s *fakeServer) Serve(net.Listener) error {
	<-s.done
	*s.events = append(*s.events, "serve_done")
	return nil
}
func (s *fakeServer) Shutdown(context.Context) error {
	*s.events = append(*s.events, "shutdown")
	close(s.done)
	return s.shutdownErr
}
func (s *fakeServer) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	*s.events = append(*s.events, "force_close")
	return nil
}

type fakeCloser struct {
	events *[]string
	err    error
	calls  int
}

type fakeWaiter struct {
	entered chan struct{}
	release chan struct{}
}

type trackedAppender struct {
	store  *session.Store
	closed chan struct{}
	calls  int
}

func (a *trackedAppender) Append(raw []byte) (*session.Record, error) { return a.store.Append(raw) }
func (a *trackedAppender) Close() error                               { a.calls++; close(a.closed); return a.store.Close() }

type requestBarrier struct{ entered, release chan struct{} }

func (b requestBarrier) Apply(*session.Record) error { close(b.entered); <-b.release; return nil }

type observedServer struct {
	*http.Server
	shutdownStarted chan struct{}
}

func (s observedServer) Shutdown(ctx context.Context) error {
	close(s.shutdownStarted)
	return s.Server.Shutdown(ctx)
}

func (w *fakeWaiter) Wait() { close(w.entered); <-w.release }

func (c *fakeCloser) Close() error {
	c.calls++
	*c.events = append(*c.events, "store_close")
	return c.err
}

func TestRunnerHandlesINTAndTERMWithShutdownBeforeOnceOnlyStoreClose(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			var events []string
			server := &fakeServer{events: &events, done: make(chan struct{})}
			closer := &fakeCloser{events: &events}
			signals := make(chan os.Signal, 2)
			signals <- signal
			signals <- signal
			err := lifecycle.Run(server, nil, closer, nil, signals, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) })
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if closer.calls != 1 {
				t.Fatalf("close calls = %d", closer.calls)
			}
			want := []string{"shutdown", "serve_done", "store_close"}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events=%v, want %v", events, want)
			}
		})
	}
}

func TestRunnerReportsStableShutdownAndCloseCodes(t *testing.T) {
	var events []string
	server := &fakeServer{events: &events, done: make(chan struct{}), shutdownErr: errors.New("secret path")}
	closer := &fakeCloser{events: &events, err: errors.New("secret close")}
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	err := lifecycle.Run(server, nil, closer, nil, signals, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) })
	if err == nil || err.Error() != "server_shutdown_failed; raw_close_failed" {
		t.Fatalf("error=%v", err)
	}
}

func TestRunnerWaitsForHandlersAfterShutdownFailureBeforeClosingAppender(t *testing.T) {
	var events []string
	server := &fakeServer{events: &events, done: make(chan struct{}), shutdownErr: errors.New("timeout")}
	closer := &fakeCloser{events: &events}
	waiter := &fakeWaiter{entered: make(chan struct{}), release: make(chan struct{})}
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	result := make(chan error, 1)
	go func() {
		result <- lifecycle.Run(server, nil, closer, waiter, signals, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) })
	}()
	<-waiter.entered
	if closer.calls != 0 {
		t.Fatalf("appender closed before handlers drained")
	}
	close(waiter.release)
	if err := <-result; err == nil {
		t.Fatal("Run succeeded despite shutdown failure")
	}
	if closer.calls != 1 {
		t.Fatalf("close calls=%d", closer.calls)
	}
}

func TestRunnerSignalsDrainBarrierBlockedAcceptedResponseBeforeOnceOnlyClose(t *testing.T) {
	for _, stopSignal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(stopSignal.String(), func(t *testing.T) {
			store, err := session.NewStore(t.TempDir(), session.WithSessionID("integrated-drain"))
			if err != nil {
				t.Fatal(err)
			}
			appender := &trackedAppender{store: store, closed: make(chan struct{})}
			now := time.Now().UTC()
			tracker := operator.NewTracker(store.SessionID(), now, time.Minute, time.Now)
			entered, release := make(chan struct{}), make(chan struct{})
			processor := capture.NewProcessor(appender, tracker, capture.WithLatest(requestBarrier{entered, release}))
			handler := gsi.NewServer(store, gsi.WithTracker(tracker), gsi.WithProcessor(processor))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			shutdownStarted := make(chan struct{})
			server := observedServer{Server: &http.Server{Handler: handler}, shutdownStarted: shutdownStarted}
			signals := make(chan os.Signal, 1)
			result := make(chan error, 1)
			go func() {
				result <- lifecycle.Run(server, listener, appender, handler, signals, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) })
			}()
			response := make(chan string, 1)
			go func() {
				resp, err := http.Post("http://"+listener.Addr().String()+"/gsi", "application/json", strings.NewReader(`{}`))
				if err != nil {
					response <- "request-error"
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				response <- string(body)
			}()
			<-entered
			signals <- stopSignal
			<-shutdownStarted
			select {
			case <-appender.closed:
				t.Fatal("appender closed while request blocked")
			default:
			}
			close(release)
			if body := <-response; body != "ok\n" {
				t.Fatalf("body=%q", body)
			}
			if err := <-result; err != nil {
				t.Fatalf("Run: %v", err)
			}
			if appender.calls != 1 {
				t.Fatalf("close calls=%d", appender.calls)
			}
		})
	}
}

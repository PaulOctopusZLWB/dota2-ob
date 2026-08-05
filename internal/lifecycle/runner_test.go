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
	"sync/atomic"
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
	select {
	case <-s.done:
	default:
		close(s.done)
	}
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

type failingHTTPServer struct {
	*http.Server
	shutdownReturned     chan struct{}
	retryShutdownStarted chan struct{}
	retryShutdownDone    chan struct{}
	closeAttempt         chan struct{}
	shutdownCalls        atomic.Int32
}

func (s *failingHTTPServer) Shutdown(ctx context.Context) error {
	if s.shutdownCalls.Add(1) == 1 {
		failedCtx, cancel := context.WithCancel(context.Background())
		cancel()
		err := s.Server.Shutdown(failedCtx)
		close(s.shutdownReturned)
		return err
	}
	close(s.retryShutdownStarted)
	err := s.Server.Shutdown(ctx)
	close(s.retryShutdownDone)
	return err
}

func (s *failingHTTPServer) Close() error {
	s.closeAttempt <- struct{}{}
	return s.Server.Close()
}

type signalingWaiter struct {
	waiter  lifecycle.Waiter
	entered chan struct{}
}

func (w signalingWaiter) Wait() {
	close(w.entered)
	w.waiter.Wait()
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

func TestRunnerShutdownFailureGracefullyFlushesRawCommittedResponseBeforeForceClose(t *testing.T) {
	store, err := session.NewStore(t.TempDir(), session.WithSessionID("shutdown-failure"))
	if err != nil {
		t.Fatal(err)
	}
	appender := &trackedAppender{store: store, closed: make(chan struct{})}
	now := time.Now().UTC()
	tracker := operator.NewTracker(store.SessionID(), now, time.Minute, time.Now)
	projectionEntered, releaseProjection := make(chan struct{}), make(chan struct{})
	processor := capture.NewProcessor(appender, tracker, capture.WithLatest(requestBarrier{projectionEntered, releaseProjection}))
	handler := gsi.NewServer(store, gsi.WithTracker(tracker), gsi.WithProcessor(processor))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	shutdownReturned := make(chan struct{})
	retryShutdownStarted := make(chan struct{})
	retryShutdownDone := make(chan struct{})
	closeAttempt := make(chan struct{}, 1)
	server := &failingHTTPServer{
		Server:               &http.Server{Handler: handler},
		shutdownReturned:     shutdownReturned,
		retryShutdownStarted: retryShutdownStarted,
		retryShutdownDone:    retryShutdownDone,
		closeAttempt:         closeAttempt,
	}
	waitEntered := make(chan struct{})
	waiter := signalingWaiter{waiter: handler, entered: waitEntered}
	signals := make(chan os.Signal, 1)
	runResult := make(chan error, 1)
	go func() {
		runResult <- lifecycle.Run(server, listener, appender, waiter, signals, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		})
	}()
	type httpResult struct {
		status int
		body   string
		err    error
	}
	response := make(chan httpResult, 1)
	go func() {
		resp, err := http.Post("http://"+listener.Addr().String()+"/gsi", "application/json", strings.NewReader(`{}`))
		if err != nil {
			response <- httpResult{err: err}
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		response <- httpResult{status: resp.StatusCode, body: string(body)}
	}()
	<-projectionEntered
	raw, err := os.ReadFile(store.RawPath())
	if err != nil || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("raw append was not committed before projection: data=%q err=%v", raw, err)
	}
	signals <- syscall.SIGTERM
	<-shutdownReturned
	select {
	case <-waitEntered:
	case <-closeAttempt:
		close(releaseProjection)
		<-response
		<-runResult
		t.Fatal("server force-close started before accepted handler drain")
	}
	close(releaseProjection)
	select {
	case <-retryShutdownStarted:
	case <-closeAttempt:
		<-response
		<-runResult
		t.Fatal("server force-close raced response finalization instead of retrying graceful shutdown")
	}
	got := <-response
	<-retryShutdownDone
	runErr := <-runResult
	if got.err != nil || got.status != http.StatusOK || got.body != "ok\n" {
		t.Fatalf("accepted response status=%d body=%q err=%v", got.status, got.body, got.err)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "server_shutdown_failed") {
		t.Fatalf("Run error=%v, want server_shutdown_failed", runErr)
	}
	if appender.calls != 1 {
		t.Fatalf("appender close calls=%d, want 1", appender.calls)
	}
	select {
	case <-closeAttempt:
		t.Fatal("server force-close was attempted after successful graceful retry")
	default:
	}
}

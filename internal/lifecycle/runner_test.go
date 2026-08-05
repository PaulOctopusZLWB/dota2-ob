package lifecycle_test

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"syscall"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
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
			err := lifecycle.Run(server, nil, closer, signals, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) })
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
	err := lifecycle.Run(server, nil, closer, signals, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) })
	if err == nil || err.Error() != "server_shutdown_failed; raw_close_failed" {
		t.Fatalf("error=%v", err)
	}
}

package lifecycle

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
)

type Server interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}
type Closer interface{ Close() error }
type ContextFactory func() (context.Context, context.CancelFunc)

func Run(server Server, listener net.Listener, appender Closer, signals <-chan os.Signal, shutdownContext ContextFactory) error {
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	var codes []string
	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			codes = append(codes, "server_serve_failed")
		}
	case <-signals:
		ctx, cancel := shutdownContext()
		err := server.Shutdown(ctx)
		cancel()
		if err != nil {
			codes = append(codes, "server_shutdown_failed")
			_ = server.Close()
		}
		serveErr := <-serveDone
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && err == nil {
			codes = append(codes, "server_serve_failed")
		}
	}
	if err := appender.Close(); err != nil {
		codes = append(codes, "raw_close_failed")
	}
	if len(codes) > 0 {
		return errors.New(strings.Join(codes, "; "))
	}
	return nil
}

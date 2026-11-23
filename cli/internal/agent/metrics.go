package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/metrics"
)

type limitedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}
type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

func serveMetrics(address string, collector *metrics.Collector, report func(string, ...any)) func() {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		report("metrics listener failed: %v", err)
		return func() {}
	}
	limited := &limitedListener{Listener: listener, slots: make(chan struct{}, 32), done: make(chan struct{})}
	server := &http.Server{Handler: metrics.Handler(collector), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(limited); err != nil && !errors.Is(err, http.ErrServerClosed) {
			report("metrics listener failed: %v", err)
		}
	}()
	report("metrics listener started")
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
		<-done
	}
}

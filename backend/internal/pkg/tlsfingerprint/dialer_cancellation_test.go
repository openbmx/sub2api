//go:build unit

package tlsfingerprint

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
)

type pausedCancellationCloseConn struct {
	net.Conn
	started chan struct{}
	release chan struct{}
}

func (c *pausedCancellationCloseConn) Close() error {
	close(c.started)
	<-c.release
	return nil
}

func TestConnectionCancellationWaitsForCloseBeforeHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn := &pausedCancellationCloseConn{started: make(chan struct{}), release: make(chan struct{})}
		finish := closeConnectionOnCancellation(ctx, conn)
		cancel()
		<-conn.started
		done := make(chan error, 1)
		go func() { done <- finish() }()
		synctest.Wait()
		var finished bool
		select {
		case <-done:
			finished = true
			t.Error("connection cleanup returned while cancellation still owned the socket")
		default:
		}
		close(conn.release)
		if !finished {
			if err := <-done; err != context.Canceled {
				t.Fatalf("closing socket must fail handoff: %v", err)
			}
		}
	})
}

func TestConnectionHandoffDisarmsCancellationClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn := &pausedCancellationCloseConn{started: make(chan struct{}), release: make(chan struct{})}
		defer close(conn.release)
		finish := closeConnectionOnCancellation(ctx, conn)
		if err := finish(); err != nil {
			t.Fatal(err)
		}
		cancel()
		synctest.Wait()
		select {
		case <-conn.started:
			t.Error("request cancellation closed an already handed-off connection")
		default:
		}
	})
}

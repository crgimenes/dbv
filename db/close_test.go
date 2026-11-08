package db

import (
	"errors"
	"io"
	"net"
	"testing"
)

func TestCloseTakesTheTunnelDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback here")
	}
	pg := &Postgres{tunnel: []io.Closer{l}}
	pg.Close()
	_, err = l.Accept()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener still open: %v", err)
	}
	pg.Close() // twice is harmless
}

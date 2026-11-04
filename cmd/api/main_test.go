package main

import (
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", "invalid")
	err := run(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "load configuration: PORT") {
		t.Fatalf("expected configuration error, got %v", err)
	}
}

func TestRunReportsOccupiedPort(t *testing.T) {
	// Holding a real listener proves startup fails visibly instead of silently
	// succeeding when the configured port is already owned by another process.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address %T", listener.Addr())
	}
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", strconv.Itoa(address.Port))
	t.Setenv("SHUTDOWN_TIMEOUT", "1s")
	err = run(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "serve HTTP:") {
		t.Fatalf("expected listener error, got %v", err)
	}
}

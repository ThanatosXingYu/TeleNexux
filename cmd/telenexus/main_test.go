package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerAppliesConnectionLimits(t *testing.T) {
	handler := http.NewServeMux()
	server := newHTTPServer("127.0.0.1:9487", handler)
	if server.Addr != "127.0.0.1:9487" || server.Handler != handler {
		t.Fatalf("server address or handler was not applied: %+v", server)
	}
	if server.ReadHeaderTimeout != 5*time.Second ||
		server.ReadTimeout != 30*time.Second ||
		server.WriteTimeout != 45*time.Second ||
		server.IdleTimeout != 2*time.Minute ||
		server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("server connection limits = %+v", server)
	}
}

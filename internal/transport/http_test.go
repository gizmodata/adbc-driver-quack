package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/message"
)

func TestEndpointCandidatesHTTPSKeepsHostname(t *testing.T) {
	uri, err := ParseURI("quack://gateway.example.com:9494?tls=true", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// No DNS lookup may happen for HTTPS — gateway.example.com does not
	// resolve here, so a lookup would fail the test.
	endpoints, err := NewHTTPTransport(uri).endpointCandidates()
	if err != nil {
		t.Fatalf("endpointCandidates: %v", err)
	}
	want := []string{"https://gateway.example.com:9494/quack"}
	if len(endpoints) != 1 || endpoints[0] != want[0] {
		t.Errorf("endpoints: got %v want %v", endpoints, want)
	}
}

func TestEndpointCandidatesHTTPExpandsAddresses(t *testing.T) {
	uri, err := ParseURI("quack://localhost:9494", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	endpoints, err := NewHTTPTransport(uri).endpointCandidates()
	if err != nil {
		t.Fatalf("endpointCandidates: %v", err)
	}
	if len(endpoints) == 0 {
		t.Fatal("expected at least one endpoint for localhost")
	}
	for _, e := range endpoints {
		if !strings.HasPrefix(e, "http://") || strings.Contains(e, "localhost") {
			t.Errorf("expected resolved-IP http endpoint, got %q", e)
		}
	}
}

func TestTransportUsesURITimeouts(t *testing.T) {
	uri, err := ParseURI("quack://h:9494?connectTimeout=3&requestTimeout=9", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tr := NewHTTPTransport(uri)
	if tr.requestTimeout != 9*time.Second {
		t.Errorf("request timeout: got %v", tr.requestTimeout)
	}
	if tr.httpClient.Timeout != 9*time.Second {
		t.Errorf("client timeout: got %v", tr.httpClient.Timeout)
	}
}

func TestSendInjectsExtraHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	tr := NewHTTPTransport(QuackURI{
		Host: u.Hostname(),
		Port: port,
		ExtraHeaders: map[string]string{
			"X-Proxy-Auth": "s3cret",
		},
	})
	// The empty 200 body fails message decoding — fine; we only assert
	// what went out on the wire.
	_, _ = tr.Send(context.Background(), message.DisconnectMessage{
		Hdr: message.MessageHeader{Type: message.MessageTypeDisconnectMessage},
	})
	if got == nil {
		t.Fatal("server saw no request")
	}
	if v := got.Get("X-Proxy-Auth"); v != "s3cret" {
		t.Errorf("X-Proxy-Auth: got %q", v)
	}
	if ct := got.Get("Content-Type"); ct != codec.DuckDBMIMEType {
		t.Errorf("Content-Type overridden: got %q", ct)
	}
}

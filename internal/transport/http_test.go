package transport

import (
	"strings"
	"testing"
	"time"
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

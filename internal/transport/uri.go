// Package transport implements the HTTP transport for the Quack
// protocol — URI parsing, address-iteration fallback for IPv4/IPv6
// loopback mismatches, and the POST-and-decode plumbing that sits
// between the ADBC driver and the wire codec.
package transport

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
)

// URLScheme is the canonical Quack ADBC URL prefix.
//
// Two forms are accepted on input:
//
//	quack://host[:port][/database][?token=…&tls=…]
//	jdbc:quack://...    (interop with quack-jdbc URLs; rare)
const URLScheme = "quack://"

// QuackURI is a parsed Quack connection URL.
type QuackURI struct {
	Host     string
	Port     int
	Database string
	TLS      bool
	Token    string
	Params   map[string]string
}

// AcceptsURL reports whether s starts with a recognized Quack URL prefix.
func AcceptsURL(s string) bool {
	return strings.HasPrefix(s, URLScheme) || strings.HasPrefix(s, "jdbc:"+URLScheme)
}

// ParseURI parses a Quack URL with optional extra options that override
// any same-named query parameters.
func ParseURI(rawURL string, opts map[string]string) (QuackURI, error) {
	if !AcceptsURL(rawURL) {
		return QuackURI{}, fmt.Errorf("transport: not a Quack URL: %q", rawURL)
	}
	stripped := rawURL
	if strings.HasPrefix(stripped, "jdbc:") {
		stripped = stripped[len("jdbc:"):]
	}
	if !strings.HasPrefix(stripped, URLScheme) {
		return QuackURI{}, fmt.Errorf("transport: malformed Quack URL: %q", rawURL)
	}
	// Replace `quack://` with `http://` so net/url parses correctly.
	parsed, err := url.Parse("http://" + stripped[len(URLScheme):])
	if err != nil {
		return QuackURI{}, fmt.Errorf("transport: invalid Quack URL %q: %w", rawURL, err)
	}
	if parsed.Hostname() == "" {
		return QuackURI{}, fmt.Errorf("transport: Quack URL has no host: %q", rawURL)
	}
	q := QuackURI{
		Host:   parsed.Hostname(),
		Port:   codec.DefaultQuackPort,
		Params: make(map[string]string),
	}
	if p := parsed.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return QuackURI{}, fmt.Errorf("transport: invalid port in %q: %w", rawURL, err)
		}
		q.Port = port
	}
	if path := strings.TrimPrefix(parsed.Path, "/"); path != "" {
		q.Database = path
	}
	for key, vals := range parsed.Query() {
		if len(vals) > 0 {
			q.Params[key] = vals[0]
		}
	}
	for k, v := range opts {
		if v != "" {
			q.Params[k] = v
		}
	}
	q.TLS = parseBool(q.Params["tls"]) || parseBool(q.Params["useEncryption"])
	q.Token = firstNonEmpty(q.Params["token"], q.Params["password"], q.Params["adbc.quack.token"])
	return q, nil
}

// HTTPURL returns the HTTP(S) endpoint a Quack request should target.
func (u QuackURI) HTTPURL() string {
	scheme := "http"
	if u.TLS {
		scheme = "https"
	}
	host := u.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // IPv6 literal
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, host, u.Port, codec.QuackEndpoint)
}

// QuackURI returns the canonical quack: URI form (no scheme prefix).
func (u QuackURI) QuackURI() string {
	return fmt.Sprintf("quack:%s:%d", u.Host, u.Port)
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

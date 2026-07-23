// Package transport implements the HTTP transport for the Quack
// protocol — URI parsing, address-iteration fallback for IPv4/IPv6
// loopback mismatches, and the POST-and-decode plumbing that sits
// between the ADBC driver and the wire codec.
package transport

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
)

// Default HTTP timeouts, matching quack-jdbc's QuackHttpTransport.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultRequestTimeout = 60 * time.Second
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
	Host           string
	Port           int
	Database       string
	TLS            bool
	Token          string
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	Params         map[string]string
	// ExtraHeaders are additional HTTP headers sent with every request,
	// e.g. for proxies or load balancers that require their own auth.
	// Mirrors duckdb-quack's EXTRA_HTTP_HEADERS secret parameter.
	ExtraHeaders map[string]string
}

// HTTPHeaderPrefix is the ADBC option prefix for extra HTTP headers:
// `adbc.quack.http.header.<Header-Name>`. Accepted only as an ADBC
// option — never as a URL query parameter, so a pasted URL cannot
// inject headers into the driver's requests.
const HTTPHeaderPrefix = "adbc.quack.http.header."

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
	// tokenEnv/tokenFile make the driver read a local secret and send it
	// to the host in the URL — accepting them from the URL would let a
	// pasted or shared URL exfiltrate an arbitrary env var or file to an
	// attacker-chosen server. ADBC options (or opts) only.
	for _, key := range []string{"tokenEnv", "tokenFile", "adbc.quack.token_env", "adbc.quack.token_file"} {
		if _, ok := q.Params[key]; ok {
			return QuackURI{}, fmt.Errorf("transport: option %q is only accepted as an ADBC option, not a URL query parameter", key)
		}
	}
	for key := range q.Params {
		if strings.HasPrefix(key, HTTPHeaderPrefix) {
			return QuackURI{}, fmt.Errorf("transport: option %q is only accepted as an ADBC option, not a URL query parameter", key)
		}
	}
	for k, v := range opts {
		if strings.HasPrefix(k, HTTPHeaderPrefix) {
			name := strings.TrimSpace(k[len(HTTPHeaderPrefix):])
			if err := validateHeader(name, v); err != nil {
				return QuackURI{}, err
			}
			if q.ExtraHeaders == nil {
				q.ExtraHeaders = make(map[string]string)
			}
			// An empty value clears a header set earlier on the database.
			if v == "" {
				delete(q.ExtraHeaders, name)
			} else {
				q.ExtraHeaders[name] = v
			}
			continue
		}
		if v != "" {
			q.Params[k] = v
		}
	}
	q.TLS = parseBool(q.Params["tls"]) || parseBool(q.Params["useEncryption"]) || parseBool(q.Params["adbc.quack.tls"])
	token, err := resolveToken(q.Params)
	if err != nil {
		return QuackURI{}, err
	}
	q.Token = token
	if q.ConnectTimeout, err = parseTimeout(q.Params, "connectTimeout", "adbc.quack.rpc.timeout_seconds.connect", DefaultConnectTimeout); err != nil {
		return QuackURI{}, err
	}
	if q.RequestTimeout, err = parseTimeout(q.Params, "requestTimeout", "adbc.quack.rpc.timeout_seconds.request", DefaultRequestTimeout); err != nil {
		return QuackURI{}, err
	}
	return q, nil
}

// validateHeader rejects header names/values that would corrupt the
// request: empty or whitespace-containing names, control characters in
// values, and the headers the protocol itself owns.
func validateHeader(name, value string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n:") {
		return fmt.Errorf("transport: invalid HTTP header name %q", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("transport: HTTP header %q value must not contain CR/LF", name)
	}
	switch strings.ToLower(name) {
	case "content-type", "accept", "content-length", "host":
		return fmt.Errorf("transport: HTTP header %q is reserved by the Quack protocol", name)
	}
	return nil
}

// resolveToken resolves the auth token with the same precedence as
// quack-jdbc's QuackTokenResolver: an explicit token (or password
// alias) wins, then a token read from the environment variable named
// by tokenEnv, then the contents of the file named by tokenFile.
func resolveToken(params map[string]string) (string, error) {
	if tok := firstNonEmpty(params["token"], params["password"], params["adbc.quack.token"]); tok != "" {
		return tok, nil
	}
	if env := firstNonEmpty(params["tokenEnv"], params["adbc.quack.token_env"]); env != "" {
		val := strings.TrimSpace(os.Getenv(env))
		if val == "" {
			return "", fmt.Errorf("transport: token environment variable is unset or empty: %s", env)
		}
		return val, nil
	}
	if file := firstNonEmpty(params["tokenFile"], params["adbc.quack.token_file"]); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("transport: read token file %s: %w", file, err)
		}
		val := strings.TrimSpace(string(raw))
		if val == "" {
			return "", fmt.Errorf("transport: token file is empty: %s", file)
		}
		return val, nil
	}
	return "", nil
}

// parseTimeout reads a timeout from params under either its JDBC-interop
// name (jdbcKey, e.g. "connectTimeout") or its ADBC option key. Plain
// digits are seconds; anything else is parsed as a Go duration ("1.5s").
func parseTimeout(params map[string]string, jdbcKey, adbcKey string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(firstNonEmpty(params[jdbcKey], params[adbcKey]))
	if raw == "" {
		return def, nil
	}
	var d time.Duration
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		d = time.Duration(secs) * time.Second
	} else if d, err = time.ParseDuration(raw); err != nil {
		return 0, fmt.Errorf("transport: option %q must be seconds or a Go duration, got %q", jdbcKey, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("transport: option %q must be positive, got %q", jdbcKey, raw)
	}
	return d, nil
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

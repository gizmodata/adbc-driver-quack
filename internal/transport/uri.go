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
	"unicode"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
)

// Default HTTP timeouts, matching quack-jdbc's QuackHttpTransport.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultRequestTimeout = 60 * time.Second
	// DefaultHeartbeatTimeout is the connection lease the driver asks the
	// server for, matching DuckDB's quack_default_heartbeat_timeout. The
	// driver renews it from a background heartbeat every third of the
	// lease, so an idle ADBC connection stays alive.
	DefaultHeartbeatTimeout = 60 * time.Second
)

// URLScheme is the canonical Quack ADBC URL prefix.
//
// Three forms are accepted on input:
//
//	quack://host[:port][/database][?token=…&tls=…]
//	quack:host[:port]   (DuckDB's own spelling, as in ATTACH 'quack:host')
//	jdbc:quack://...    (interop with quack-jdbc URLs; rare)
const URLScheme = "quack://"

// QuackURI is a parsed Quack connection URL.
type QuackURI struct {
	Host     string
	Port     int
	Database string
	// TLS selects HTTPS. Unless set explicitly (tls=true/false), it
	// follows DuckDB 2.0's rule: plain HTTP for localhost / 127.0.0.1 /
	// ::1, HTTPS for every other host. A pinned fingerprint implies TLS.
	TLS bool
	// SSLFingerprint, when set, is the SHA-256 fingerprint (64 uppercase
	// hex digits) of the one server certificate to trust, instead of CA
	// verification: how a client authenticates a quack_serve self-signed
	// certificate.
	SSLFingerprint string
	// ClientID is sent in the handshake; empty or at least 4 characters.
	ClientID string
	// HeartbeatTimeout is the connection lease requested from the server.
	HeartbeatTimeout time.Duration
	Token            string
	ConnectTimeout   time.Duration
	RequestTimeout   time.Duration
	Params           map[string]string
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
	return strings.HasPrefix(s, "quack:") || strings.HasPrefix(s, "jdbc:"+URLScheme)
}

// ParseURI parses a Quack URL with optional extra options that override
// any same-named query parameters.
func ParseURI(rawURL string, opts map[string]string) (QuackURI, error) {
	if !AcceptsURL(rawURL) {
		return QuackURI{}, fmt.Errorf("transport: not a Quack URL: %q", rawURL)
	}
	stripped := strings.TrimSpace(rawURL)
	if strings.HasPrefix(stripped, "jdbc:") {
		stripped = stripped[len("jdbc:"):]
	}
	if !strings.HasPrefix(stripped, URLScheme) && strings.HasPrefix(stripped, "quack:") {
		// DuckDB's `quack:host[:port]` spelling.
		stripped = URLScheme + stripped[len("quack:"):]
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
	if q.SSLFingerprint, err = normalizeFingerprint(firstNonEmpty(q.Params["ssl_fingerprint"], q.Params["sslFingerprint"], q.Params["adbc.quack.ssl_fingerprint"])); err != nil {
		return QuackURI{}, err
	}
	tlsRaw := firstNonEmpty(q.Params["adbc.quack.tls"], q.Params["tls"], q.Params["useEncryption"])
	switch {
	case tlsRaw != "":
		q.TLS = parseBool(tlsRaw)
		if !q.TLS && q.SSLFingerprint != "" {
			return QuackURI{}, fmt.Errorf("transport: ssl_fingerprint pins the server's TLS certificate and cannot be combined with tls=false")
		}
	case q.SSLFingerprint != "":
		q.TLS = true
	default:
		q.TLS = !IsLocalHost(q.Host)
	}
	q.ClientID = firstNonEmpty(q.Params["adbc.quack.client_id"], q.Params["clientId"], q.Params["client_id"])
	if q.ClientID != "" && len(q.ClientID) < 4 {
		return QuackURI{}, fmt.Errorf("transport: client_id must be empty or at least 4 characters")
	}
	if q.HeartbeatTimeout, err = parseTimeout(q.Params, "heartbeatTimeout", "adbc.quack.heartbeat_timeout_seconds", DefaultHeartbeatTimeout); err != nil {
		return QuackURI{}, err
	}
	if q.HeartbeatTimeout < time.Second {
		return QuackURI{}, fmt.Errorf("transport: heartbeat timeout must be at least 1 second")
	}
	var token string
	token, err = resolveToken(q.Params)
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

// IsLocalHost reports whether host is a loopback name DuckDB serves over
// plain HTTP by default.
func IsLocalHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// normalizeFingerprint mirrors QuackUri::NormalizeFingerprint: accept the
// colon-separated form `openssl x509 -fingerprint -sha256` prints, with or
// without a `sha256:` prefix, and return 64 uppercase hex digits.
func normalizeFingerprint(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(strings.ToLower(s), "sha256:") {
		s = s[len("sha256:"):]
	}
	var b strings.Builder
	for _, c := range s {
		switch {
		case c == ':' || c == ' ':
			continue
		case unicode.Is(unicode.ASCII_Hex_Digit, c):
			b.WriteRune(unicode.ToUpper(c))
		default:
			return "", fmt.Errorf("transport: invalid ssl_fingerprint %q: expected the certificate's SHA-256 fingerprint as hex digits", raw)
		}
	}
	if b.Len() != 64 {
		return "", fmt.Errorf("transport: invalid ssl_fingerprint %q: a SHA-256 fingerprint has 64 hex digits, got %d", raw, b.Len())
	}
	return b.String(), nil
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

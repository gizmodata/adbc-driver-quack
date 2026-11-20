package transport

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Driver-specific option keys. They follow the ADBC Driver Foundry style
// guide (https://adbc-drivers.org/building-drivers/style-guide.html):
// `quack[.group].option`, never starting with `adbc.` (reserved for options
// the ADBC specification defines, such as `uri`, `username`, `password`).
const (
	OptionAuthToken     = "quack.auth.token"
	OptionAuthTokenEnv  = "quack.auth.token_env"
	OptionAuthTokenFile = "quack.auth.token_file"

	OptionTLSEnabled                = "quack.tls.enabled"
	OptionTLSCertificateFingerprint = "quack.tls.certificate_fingerprint"

	OptionSessionHeartbeatTimeout = "quack.session.heartbeat_timeout_seconds"
	OptionSessionClientID         = "quack.session.client_id"

	OptionHTTPConnectTimeout = "quack.http.connect_timeout_seconds"
	OptionHTTPRequestTimeout = "quack.http.request_timeout_seconds"
)

// HTTPHeaderPrefix is the option prefix for extra HTTP headers:
// `quack.http.header.<Header-Name>`. Accepted only as an ADBC option, never
// as a URL query parameter, so a pasted URL cannot inject headers into the
// driver's requests.
const HTTPHeaderPrefix = "quack.http.header."

// DeprecatedHTTPHeaderPrefix is the pre-1.1 spelling of HTTPHeaderPrefix.
const DeprecatedHTTPHeaderPrefix = "adbc.quack.http.header."

// DeprecatedOptionPrefix starts every option key from before 1.1.
const DeprecatedOptionPrefix = "adbc.quack."

// deprecatedOptions maps each pre-1.1 option key to its current name. The old
// keys keep working (set and get) and print a deprecation warning once each.
var deprecatedOptions = map[string]string{
	"adbc.quack.token":                       OptionAuthToken,
	"adbc.quack.token_env":                   OptionAuthTokenEnv,
	"adbc.quack.token_file":                  OptionAuthTokenFile,
	"adbc.quack.tls":                         OptionTLSEnabled,
	"adbc.quack.ssl_fingerprint":             OptionTLSCertificateFingerprint,
	"adbc.quack.heartbeat_timeout_seconds":   OptionSessionHeartbeatTimeout,
	"adbc.quack.client_id":                   OptionSessionClientID,
	"adbc.quack.rpc.timeout_seconds.connect": OptionHTTPConnectTimeout,
	"adbc.quack.rpc.timeout_seconds.request": OptionHTTPRequestTimeout,
}

// DeprecatedOptions returns a copy of the old -> new option key table.
func DeprecatedOptions() map[string]string {
	out := make(map[string]string, len(deprecatedOptions))
	for k, v := range deprecatedOptions {
		out[k] = v
	}
	return out
}

// KnownOption reports whether key is a current driver-specific option key
// (including any `quack.http.header.<Name>`).
func KnownOption(key string) bool {
	switch key {
	case OptionAuthToken, OptionAuthTokenEnv, OptionAuthTokenFile,
		OptionTLSEnabled, OptionTLSCertificateFingerprint,
		OptionSessionHeartbeatTimeout, OptionSessionClientID,
		OptionHTTPConnectTimeout, OptionHTTPRequestTimeout:
		return true
	}
	return strings.HasPrefix(key, HTTPHeaderPrefix)
}

// CanonicalOption returns the current name for key and whether key is a
// deprecated spelling of it. Any other key is returned unchanged.
func CanonicalOption(key string) (canonical string, deprecated bool) {
	if newKey, ok := deprecatedOptions[key]; ok {
		return newKey, true
	}
	if strings.HasPrefix(key, DeprecatedHTTPHeaderPrefix) {
		return HTTPHeaderPrefix + key[len(DeprecatedHTTPHeaderPrefix):], true
	}
	return key, false
}

// Deprecation warnings: one line per old key per process, to deprecationOut.
var (
	deprecationMu   sync.Mutex
	deprecationOut  io.Writer = os.Stderr
	deprecationSeen sync.Map  // old key (or old header prefix) -> struct{}
)

// WarnDeprecatedOption writes `[quack] option "<old>" is deprecated; use
// "<new>"` the first time in this process that oldKey is used. Only key
// names are written, never values: options carry tokens and passwords.
func WarnDeprecatedOption(oldKey, newKey string) {
	if _, seen := deprecationSeen.LoadOrStore(oldKey, struct{}{}); seen {
		return
	}
	deprecationMu.Lock()
	defer deprecationMu.Unlock()
	_, _ = fmt.Fprintf(deprecationOut, "[quack] option %q is deprecated; use %q\n", oldKey, newKey)
}

// SetDeprecationOutput redirects deprecation warnings (tests) and forgets
// which keys have already been reported. It returns a function that restores
// the previous writer.
func SetDeprecationOutput(w io.Writer) (restore func()) {
	deprecationMu.Lock()
	prev := deprecationOut
	deprecationOut = w
	deprecationMu.Unlock()
	deprecationSeen.Clear()
	return func() {
		deprecationMu.Lock()
		deprecationOut = prev
		deprecationMu.Unlock()
		deprecationSeen.Clear()
	}
}

// canonicalOptions rewrites deprecated keys in opts to their current names,
// warning once per old key. When both spellings are present the current
// name wins.
func canonicalOptions(opts map[string]string) map[string]string {
	out := make(map[string]string, len(opts))
	for k, v := range opts {
		newKey, deprecated := CanonicalOption(k)
		if !deprecated {
			out[k] = v
			continue
		}
		WarnDeprecatedOption(k, newKey)
		if _, both := opts[newKey]; both {
			continue
		}
		out[newKey] = v
	}
	return out
}

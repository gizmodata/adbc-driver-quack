package transport

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
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

// CheckOptionKey refuses a key that looks like one of this driver's options
// (`quack.*`, or the pre-1.1 `adbc.quack.*`) but is not one, so a
// misspelled or half-migrated key (`adbc.quack.tls.enabled`) fails instead
// of being ignored. The error names the key exactly as the caller wrote it.
func CheckOptionKey(key string) error {
	if KnownOption(key) {
		return nil
	}
	if _, deprecated := CanonicalOption(key); deprecated {
		return nil
	}
	if strings.HasPrefix(key, "quack.") || strings.HasPrefix(key, DeprecatedOptionPrefix) {
		return fmt.Errorf("transport: unsupported option %q", key)
	}
	return nil
}

// headerOption returns the header name in key and whether key spells it
// with the current prefix; ok is false for a key that is not a header option.
func headerOption(key string) (name string, current bool, ok bool) {
	switch {
	case strings.HasPrefix(key, HTTPHeaderPrefix):
		return strings.TrimSpace(key[len(HTTPHeaderPrefix):]), true, true
	case strings.HasPrefix(key, DeprecatedHTTPHeaderPrefix):
		return strings.TrimSpace(key[len(DeprecatedHTTPHeaderPrefix):]), false, true
	}
	return "", false, false
}

// headerOptions collects the extra HTTP headers in opts, keyed by canonical
// header name (HTTP header names are case-insensitive). Two keys under the
// same prefix that name the same header (differing only in case) are refused,
// so which one is sent never depends on map order. Between the deprecated and
// the current spelling the current one wins, unless its value is empty and the
// deprecated one's is not. An empty value sends no header.
func headerOptions(opts map[string]string) (map[string]string, error) {
	type pick struct {
		key, value string
	}
	current := map[string]pick{}
	deprecated := map[string]pick{}
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name, isCurrent, ok := headerOption(k)
		if !ok {
			continue
		}
		v := opts[k]
		if err := validateHeader(name, v); err != nil {
			return nil, err
		}
		canon := http.CanonicalHeaderKey(name)
		group := deprecated
		if isCurrent {
			group = current
		} else {
			WarnDeprecatedOption(k, HTTPHeaderPrefix+name)
		}
		if prev, dup := group[canon]; dup {
			return nil, fmt.Errorf("transport: options %q and %q name the same HTTP header; set it once", prev.key, k)
		}
		group[canon] = pick{key: k, value: v}
	}
	var out map[string]string
	set := func(canon, v string) {
		if v == "" {
			return
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[canon] = v
	}
	for canon, p := range current {
		if p.value == "" {
			if old, ok := deprecated[canon]; ok {
				set(canon, old.value)
				continue
			}
		}
		set(canon, p.value)
	}
	for canon, old := range deprecated {
		if _, ok := current[canon]; !ok {
			set(canon, old.value)
		}
	}
	return out, nil
}

// canonicalOptions rewrites deprecated keys in opts to their current names,
// warning once per old key, and returns, for each current name it set, the
// key as the caller spelled it (for error messages). When both spellings are
// given the current name wins, unless its value is empty and the deprecated
// one's is not: an empty value never erases a non-empty one (an empty
// certificate fingerprint must not drop a pin, nor an empty token a token).
// Header options are left to headerOptions.
func canonicalOptions(opts map[string]string) (out map[string]string, spelled map[string]string) {
	out = make(map[string]string, len(opts))
	spelled = make(map[string]string, len(opts))
	for k, v := range opts {
		if _, _, header := headerOption(k); header {
			continue
		}
		if _, deprecated := CanonicalOption(k); !deprecated {
			out[k] = v
			spelled[k] = k
		}
	}
	for k, v := range opts {
		if _, _, header := headerOption(k); header {
			continue
		}
		newKey, deprecated := CanonicalOption(k)
		if !deprecated {
			continue
		}
		WarnDeprecatedOption(k, newKey)
		if cur, both := out[newKey]; both && (cur != "" || v == "") {
			continue
		}
		out[newKey] = v
		spelled[newKey] = k
	}
	return out, spelled
}

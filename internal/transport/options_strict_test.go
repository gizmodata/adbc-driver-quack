package transport

import (
	"net/http"
	"strings"
	"testing"
)

const testFingerprint = "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"

// F1: an empty value under the new name does not erase a non-empty value under
// the deprecated name (it used to drop a certificate pin, falling back to plain
// HTTP on loopback, and erase a token).
func TestEmptyNewKeyDoesNotEraseDeprecatedValue(t *testing.T) {
	captureDeprecations(t)
	for _, host := range []string{"127.0.0.1", "db.example.com"} {
		for i := 0; i < 20; i++ { // map order is random
			u, err := ParseURI("quack://"+host+":9494", map[string]string{
				"adbc.quack.ssl_fingerprint":    testFingerprint,
				OptionTLSCertificateFingerprint: "",
			})
			if err != nil || !u.TLS || u.SSLFingerprint == "" {
				t.Fatalf("%s: the pin was dropped: TLS=%v pin=%q err=%v", host, u.TLS, u.SSLFingerprint, err)
			}
		}
	}
	for i := 0; i < 20; i++ {
		u, err := ParseURI("quack://db.example.com", map[string]string{"adbc.quack.token": "s3cret", OptionAuthToken: ""})
		if err != nil || u.Token != "s3cret" {
			t.Fatalf("an empty new token erased the old one: %q, %v", u.Token, err)
		}
		u, err = ParseURI("quack://db.example.com", map[string]string{"adbc.quack.token": "old", OptionAuthToken: "new"})
		if err != nil || u.Token != "new" {
			t.Fatalf("a non-empty new token must win: %q, %v", u.Token, err)
		}
	}
	// The same rule for a header: an empty new value doesn't clear the old one.
	u, err := ParseURI("quack://db.example.com", map[string]string{
		"adbc.quack.http.header.X-A": "old", HTTPHeaderPrefix + "X-A": ""})
	if err != nil || u.ExtraHeaders["X-A"] != "old" {
		t.Fatalf("headers %v, err %v", u.ExtraHeaders, err)
	}
}

// F2: header names are case-insensitive. Old and new spellings that differ only
// in case resolve deterministically (the new one wins); two keys under one
// prefix naming the same header are refused.
func TestHeaderNamesCompareCaseInsensitively(t *testing.T) {
	captureDeprecations(t)
	for i := 0; i < 200; i++ {
		u, err := ParseURI("quack://db.example.com", map[string]string{
			"adbc.quack.http.header.x-proxy-auth": "OLD",
			HTTPHeaderPrefix + "X-Proxy-Auth":     "NEW",
		})
		if err != nil {
			t.Fatal(err)
		}
		h := http.Header{}
		for n, v := range u.ExtraHeaders {
			h.Set(n, v)
		}
		if len(u.ExtraHeaders) != 1 || h.Get("X-Proxy-Auth") != "NEW" {
			t.Fatalf("run %d: headers %v, want only X-Proxy-Auth: NEW", i, u.ExtraHeaders)
		}
	}
	for _, pair := range [][2]string{
		{HTTPHeaderPrefix + "X-Proxy-Auth", HTTPHeaderPrefix + "x-proxy-auth"},
		{"adbc.quack.http.header.X-Proxy-Auth", "adbc.quack.http.header.X-PROXY-AUTH"},
	} {
		for i := 0; i < 20; i++ {
			_, err := ParseURI("quack://db.example.com", map[string]string{pair[0]: "a", pair[1]: "b"})
			if err == nil || !strings.Contains(err.Error(), "name the same HTTP header") {
				t.Fatalf("%v: want a refusal, got %v", pair, err)
			}
			if strings.Contains(err.Error(), `"a"`) || strings.Contains(err.Error(), `"b"`) {
				t.Fatalf("the error carries a header value: %v", err)
			}
		}
	}
}

// F3: keys under this driver's prefixes that are not options are refused, as
// written, in the options and in the URL (they used to be ignored, so a
// half-migrated `adbc.quack.tls.enabled=true` left TLS off).
func TestUnknownDriverKeysAreRefused(t *testing.T) {
	captureDeprecations(t)
	for _, key := range []string{"adbc.quack.tls.enabled", "adbc.quack.tls.certificate_fingerprint",
		"adbc.quack.ssl_fingerprnt", "quack.tls.enable", "quack.nope"} {
		_, err := ParseURI("quack://127.0.0.1:9494", map[string]string{key: "true"})
		if err == nil || !strings.Contains(err.Error(), `unsupported option "`+key+`"`) {
			t.Errorf("option %q: want a refusal naming it, got %v", key, err)
		}
		_, err = ParseURI("quack://127.0.0.1:9494?"+key+"=true", nil)
		if err == nil || !strings.Contains(err.Error(), `unsupported option "`+key+`"`) {
			t.Errorf("URL %q: want a refusal naming it, got %v", key, err)
		}
	}
	// Interop query parameters and spec keys still pass.
	if _, err := ParseURI("quack://h?token=x&tls=true&clientId=abcd", map[string]string{"username": "u"}); err != nil {
		t.Fatal(err)
	}
}

// F4: a boolean option accepts true/false/1/0/yes/no/on/off in any case and
// refuses anything else, naming the key as written (it used to read a typo as
// false: plain HTTP to a remote host).
func TestBooleanOptionsRefuseUnknownValues(t *testing.T) {
	captureDeprecations(t)
	for _, key := range []string{"adbc.quack.tls", OptionTLSEnabled, "tls"} {
		_, err := ParseURI("quack://db.example.com", map[string]string{key: "ture"})
		if err == nil || !strings.Contains(err.Error(), `option "`+key+`" must be true or false`) {
			t.Errorf("%s=ture: want a refusal naming %q, got %v", key, key, err)
		}
	}
	_, err := ParseURI("quack://db.example.com?tls=nope", nil)
	if err == nil || !strings.Contains(err.Error(), `option "tls" must be true or false`) {
		t.Errorf("URL tls=nope: %v", err)
	}
	for value, want := range map[string]bool{"true": true, "TRUE": true, "1": true, "yes": true, "On": true,
		"false": false, "False": false, "0": false, "no": false, "OFF": false} {
		u, err := ParseURI("quack://db.example.com", map[string]string{OptionTLSEnabled: value})
		if err != nil || u.TLS != want {
			t.Errorf("%s=%s: TLS=%v err=%v, want %v", OptionTLSEnabled, value, u.TLS, err, want)
		}
	}
}

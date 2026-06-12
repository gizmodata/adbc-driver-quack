package transport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBasicURL(t *testing.T) {
	u, err := ParseURI("quack://example.com", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "example.com" {
		t.Errorf("host: got %q", u.Host)
	}
	if u.Port != 9494 {
		t.Errorf("port: got %d", u.Port)
	}
	if u.TLS {
		t.Errorf("tls: expected false")
	}
}

func TestParseFullURL(t *testing.T) {
	u, err := ParseURI("quack://h.example:1234/mydb?token=abc&tls=true", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "h.example" || u.Port != 1234 {
		t.Errorf("host:port = %s:%d", u.Host, u.Port)
	}
	if u.Database != "mydb" {
		t.Errorf("database: got %q", u.Database)
	}
	if u.Token != "abc" {
		t.Errorf("token: got %q", u.Token)
	}
	if !u.TLS {
		t.Errorf("tls: expected true")
	}
	if want := "https://h.example:1234/quack"; u.HTTPURL() != want {
		t.Errorf("httpURL: got %q want %q", u.HTTPURL(), want)
	}
}

func TestOptsOverrideParams(t *testing.T) {
	u, err := ParseURI("quack://h:9494", map[string]string{"token": "from-opts"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Token != "from-opts" {
		t.Errorf("token: got %q", u.Token)
	}
}

func TestADBCTLSOption(t *testing.T) {
	u, err := ParseURI("quack://h:9494", map[string]string{"adbc.quack.tls": "true"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !u.TLS {
		t.Errorf("tls: expected true from adbc.quack.tls option")
	}
}

func TestTokenEnv(t *testing.T) {
	t.Setenv("QUACK_TEST_TOKEN", "  secret-from-env\n")
	u, err := ParseURI("quack://h:9494", map[string]string{"adbc.quack.token_env": "QUACK_TEST_TOKEN"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Token != "secret-from-env" {
		t.Errorf("token: got %q", u.Token)
	}
}

func TestTokenEnvUnset(t *testing.T) {
	_, err := ParseURI("quack://h:9494", map[string]string{"adbc.quack.token_env": "QUACK_TEST_TOKEN_UNSET"})
	if err == nil || !strings.Contains(err.Error(), "unset or empty") {
		t.Errorf("expected unset-env error, got %v", err)
	}
}

func TestTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := ParseURI("quack://h:9494", map[string]string{"adbc.quack.token_file": path})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Token != "secret-from-file" {
		t.Errorf("token: got %q", u.Token)
	}
}

func TestTokenFileMissingOrEmpty(t *testing.T) {
	if _, err := ParseURI("quack://h:9494", map[string]string{"adbc.quack.token_file": filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Errorf("expected error for missing token file")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseURI("quack://h:9494", map[string]string{"adbc.quack.token_file": empty}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty-file error, got %v", err)
	}
}

func TestTokenPrecedence(t *testing.T) {
	t.Setenv("QUACK_TEST_TOKEN", "from-env")
	u, err := ParseURI("quack://h:9494", map[string]string{
		"adbc.quack.token":     "direct",
		"adbc.quack.token_env": "QUACK_TEST_TOKEN",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Token != "direct" {
		t.Errorf("token: got %q, want direct token to win", u.Token)
	}
}

func TestTokenEnvFileRejectedInURL(t *testing.T) {
	for _, q := range []string{"tokenEnv=PATH", "tokenFile=/etc/passwd", "adbc.quack.token_env=PATH", "adbc.quack.token_file=/etc/passwd"} {
		if _, err := ParseURI("quack://h:9494?"+q, nil); err == nil {
			t.Errorf("expected %q in URL to be rejected", q)
		}
	}
}

func TestTimeoutDefaults(t *testing.T) {
	u, err := ParseURI("quack://h:9494", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.ConnectTimeout != DefaultConnectTimeout || u.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("timeouts: got %v/%v", u.ConnectTimeout, u.RequestTimeout)
	}
}

func TestTimeoutParsing(t *testing.T) {
	u, err := ParseURI("quack://h:9494?connectTimeout=5", map[string]string{
		"adbc.quack.rpc.timeout_seconds.request": "90",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.ConnectTimeout != 5*time.Second {
		t.Errorf("connect timeout: got %v", u.ConnectTimeout)
	}
	if u.RequestTimeout != 90*time.Second {
		t.Errorf("request timeout: got %v", u.RequestTimeout)
	}

	u, err = ParseURI("quack://h:9494", map[string]string{"adbc.quack.rpc.timeout_seconds.connect": "1.5s"})
	if err != nil {
		t.Fatalf("parse duration form: %v", err)
	}
	if u.ConnectTimeout != 1500*time.Millisecond {
		t.Errorf("connect timeout: got %v", u.ConnectTimeout)
	}

	for _, bad := range []string{"0", "-3", "soon"} {
		if _, err := ParseURI("quack://h:9494?requestTimeout="+bad, nil); err == nil {
			t.Errorf("expected error for requestTimeout=%q", bad)
		}
	}
}

func TestAcceptsURL(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"quack://localhost", true},
		{"jdbc:quack://localhost", true},
		{"http://example.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := AcceptsURL(c.s); got != c.want {
			t.Errorf("AcceptsURL(%q): got %v want %v", c.s, got, c.want)
		}
	}
}

func TestIPv6Host(t *testing.T) {
	u, err := ParseURI("quack://[::1]:9494", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "::1" {
		t.Errorf("host: got %q", u.Host)
	}
	if want := "http://[::1]:9494/quack"; u.HTTPURL() != want {
		t.Errorf("httpURL: got %q want %q", u.HTTPURL(), want)
	}
}

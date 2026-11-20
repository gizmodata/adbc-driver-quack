package transport

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureDeprecations sends deprecation warnings to a buffer for one test and
// forgets which keys were reported, so each test sees its own first use.
func captureDeprecations(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf syncBuffer
	restore := SetDeprecationOutput(&buf)
	t.Cleanup(restore)
	return &buf.Buffer
}

type syncBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func TestOptionKeysFollowTheStyleGuide(t *testing.T) {
	for old, current := range DeprecatedOptions() {
		if !strings.HasPrefix(old, "adbc.quack.") {
			t.Errorf("old key %q: want the adbc.quack. prefix", old)
		}
		if strings.HasPrefix(current, "adbc.") || !strings.HasPrefix(current, "quack.") {
			t.Errorf("new key %q for %q: must start with quack., never adbc.", current, old)
		}
		if !KnownOption(current) {
			t.Errorf("new key %q is not a known option", current)
		}
		if KnownOption(old) {
			t.Errorf("old key %q must not count as a current option", old)
		}
	}
	if !KnownOption(HTTPHeaderPrefix + "X-Proxy-Auth") {
		t.Error("a header option is a known option")
	}
}

// Every old key configures exactly what its new name does.
func TestDeprecatedKeysSetTheSameThing(t *testing.T) {
	captureDeprecations(t)
	values := map[string]string{
		OptionAuthToken:                 "tok-123",
		OptionTLSEnabled:                "true",
		OptionTLSCertificateFingerprint: strings.Repeat("ab", 32),
		OptionSessionHeartbeatTimeout:   "30",
		OptionSessionClientID:           "etl-client",
		OptionHTTPConnectTimeout:        "3",
		OptionHTTPRequestTimeout:        "1.5s",
	}
	for old, current := range DeprecatedOptions() {
		v, ok := values[current]
		if !ok {
			continue // token_env / token_file: below
		}
		t.Run(old, func(t *testing.T) {
			viaNew, err := ParseURI("quack://h:9494", map[string]string{current: v})
			if err != nil {
				t.Fatal(err)
			}
			viaOld, err := ParseURI("quack://h:9494", map[string]string{old: v})
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%+v", viaNew) != fmt.Sprintf("%+v", viaOld) {
				t.Fatalf("old key %q parsed differently from %q:\nnew %+v\nold %+v", old, current, viaNew, viaOld)
			}
			if fmt.Sprintf("%+v", viaNew) == fmt.Sprintf("%+v", defaultURI()) {
				t.Fatal("the option changed nothing")
			}
		})
	}
	t.Setenv("QUACK_TEST_TOKEN_ALIAS", "from-env")
	u, err := ParseURI("quack://h", map[string]string{"adbc.quack.token_env": "QUACK_TEST_TOKEN_ALIAS"})
	if err != nil || u.Token != "from-env" {
		t.Fatalf("adbc.quack.token_env: token %q, err %v", u.Token, err)
	}
	u, err = ParseURI("quack://h", map[string]string{"adbc.quack.http.header.X-Proxy-Auth": "s"})
	if err != nil || u.ExtraHeaders["X-Proxy-Auth"] != "s" {
		t.Fatalf("adbc.quack.http.header.: headers %v, err %v", u.ExtraHeaders, err)
	}
	// The old keys are also read from the URL, as before.
	u, err = ParseURI("quack://h?adbc.quack.client_id=from-url", nil)
	if err != nil || u.ClientID != "from-url" {
		t.Fatalf("adbc.quack.client_id in the URL: client id %q, err %v", u.ClientID, err)
	}
}

// defaultURI is quack://h:9494 parsed with no options.
func defaultURI() QuackURI {
	u, _ := ParseURI("quack://h:9494", nil)
	return u
}

func TestNewKeyWinsOverDeprecatedKey(t *testing.T) {
	captureDeprecations(t)
	opts := map[string]string{
		"adbc.quack.client_id":                 "old-client",
		OptionSessionClientID:                  "new-client",
		"adbc.quack.heartbeat_timeout_seconds": "5",
		OptionSessionHeartbeatTimeout:          "7",
		"adbc.quack.token":                     "old-token",
		OptionAuthToken:                        "new-token",
		"adbc.quack.http.header.X-A":           "old-header",
		HTTPHeaderPrefix + "X-A":               "new-header",
	}
	// Map order is random: parse many times so either order is exercised.
	for i := 0; i < 50; i++ {
		u, err := ParseURI("quack://h", opts)
		if err != nil {
			t.Fatal(err)
		}
		if u.ClientID != "new-client" || u.HeartbeatTimeout != 7*time.Second || u.Token != "new-token" ||
			u.ExtraHeaders["X-A"] != "new-header" {
			t.Fatalf("run %d: the deprecated key won: %+v", i, u)
		}
	}
	u, err := ParseURI("quack://h?adbc.quack.client_id=old-client&quack.session.client_id=new-client", nil)
	if err != nil || u.ClientID != "new-client" {
		t.Fatalf("URL with both keys: client id %q, err %v", u.ClientID, err)
	}
}

func TestDeprecatedKeysRefusedOnTheURLLikeTheirNewNames(t *testing.T) {
	captureDeprecations(t)
	for _, q := range []string{"adbc.quack.token_env=PATH", "adbc.quack.token_file=/etc/passwd", "adbc.quack.http.header.X-Evil=1"} {
		_, err := ParseURI("quack://h:9494?"+q, nil)
		if err == nil || !strings.Contains(err.Error(), "only accepted as an ADBC option") {
			t.Errorf("%s: want a refusal, got %v", q, err)
		}
	}
}

func TestDeprecationWarningOncePerKeyWithoutValues(t *testing.T) {
	buf := captureDeprecations(t)
	const secret = "s3cr3t-token-value"
	for i := 0; i < 3; i++ {
		if _, err := ParseURI("quack://h", map[string]string{
			"adbc.quack.token":                    secret,
			"adbc.quack.http.header.X-Proxy-Auth": secret + "-header",
			"adbc.quack.client_id":                "abcd",
		}); err != nil {
			t.Fatal(err)
		}
	}
	got := buf.String()
	want := []string{
		`[quack] option "adbc.quack.token" is deprecated; use "quack.auth.token"`,
		`[quack] option "adbc.quack.http.header.X-Proxy-Auth" is deprecated; use "quack.http.header.X-Proxy-Auth"`,
		`[quack] option "adbc.quack.client_id" is deprecated; use "quack.session.client_id"`,
	}
	for _, w := range want {
		if strings.Count(got, w+"\n") != 1 {
			t.Errorf("want exactly one line %q, got:\n%s", w, got)
		}
	}
	if n := strings.Count(got, "\n"); n != len(want) {
		t.Errorf("want %d lines, got %d:\n%s", len(want), n, got)
	}
	if strings.Contains(got, secret) || strings.Contains(got, "abcd") {
		t.Errorf("a warning carries an option value:\n%s", got)
	}
}

func TestDeprecationWarningOnceUnderConcurrency(t *testing.T) {
	buf := captureDeprecations(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			WarnDeprecatedOption("adbc.quack.tls", OptionTLSEnabled)
		}()
	}
	wg.Wait()
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("want one warning from 32 goroutines, got %d:\n%s", n, buf.String())
	}
}

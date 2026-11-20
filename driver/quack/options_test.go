package quack

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"

	"github.com/gizmodata/adbc-driver-quack/internal/transport"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureWarnings(t *testing.T) *lockedBuffer {
	t.Helper()
	var b lockedBuffer
	t.Cleanup(transport.SetDeprecationOutput(&b))
	return &b
}

func newTestDatabase(t *testing.T, opts map[string]string) *databaseImpl {
	t.Helper()
	db, err := NewDriver(nil).NewDatabase(opts)
	if err != nil {
		t.Fatal(err)
	}
	return db.(*databaseImpl)
}

func adbcStatus(t *testing.T, err error) adbc.Status {
	t.Helper()
	var ae adbc.Error
	if !errors.As(err, &ae) {
		t.Fatalf("want an adbc.Error, got %T: %v", err, err)
	}
	if !strings.HasPrefix(ae.Msg, "[quack] ") {
		t.Errorf("error message %q does not start with [quack]", ae.Msg)
	}
	return ae.Code
}

// GetOption answers under the old and the new key, whichever was used to set.
func TestDatabaseGetOptionViaOldAndNewKeys(t *testing.T) {
	captureWarnings(t)
	for old, current := range DeprecatedOptions() {
		if secretOption(current) {
			continue
		}
		for _, setKey := range []string{old, current} {
			db := newTestDatabase(t, map[string]string{setKey: "value-1"})
			for _, getKey := range []string{old, current} {
				got, err := db.GetOption(getKey)
				if err != nil || got != "value-1" {
					t.Errorf("set %q, get %q: got %q, %v", setKey, getKey, got, err)
				}
			}
		}
	}
}

func TestDatabaseNewKeyWinsWhenBothAreSet(t *testing.T) {
	captureWarnings(t)
	// Both in one call, and one after the other in either order.
	both := newTestDatabase(t, map[string]string{"adbc.quack.client_id": "old-id", OptionClientID: "new-id"})
	oldThenNew := newTestDatabase(t, map[string]string{"adbc.quack.client_id": "old-id"})
	if err := oldThenNew.SetOption(OptionClientID, "new-id"); err != nil {
		t.Fatal(err)
	}
	newThenOld := newTestDatabase(t, map[string]string{OptionClientID: "new-id"})
	if err := newThenOld.SetOption("adbc.quack.client_id", "old-id"); err != nil {
		t.Fatal(err)
	}
	for name, db := range map[string]*databaseImpl{"both": both, "old then new": oldThenNew, "new then old": newThenOld} {
		for _, key := range []string{"adbc.quack.client_id", OptionClientID} {
			if got, err := db.GetOption(key); err != nil || got != "new-id" {
				t.Errorf("%s: GetOption(%q) = %q, %v; want the new key's value", name, key, got, err)
			}
		}
		// What Open hands the transport resolves the same way.
		u, err := transport.ParseURI("quack://h", cloneMap(db.opts))
		if err != nil || u.ClientID != "new-id" {
			t.Errorf("%s: connection would use client id %q (%v)", name, u.ClientID, err)
		}
	}
}

func TestDatabaseSecretsStayHiddenUnderOldKeys(t *testing.T) {
	captureWarnings(t)
	db := newTestDatabase(t, map[string]string{
		"adbc.quack.token":             "tok",
		"adbc.quack.http.header.X-A":   "hdr",
		OptionHTTPHeaderPrefix + "X-B": "hdr2",
	})
	for _, key := range []string{"adbc.quack.token", OptionToken, "adbc.quack.http.header.X-A", OptionHTTPHeaderPrefix + "X-A", OptionHTTPHeaderPrefix + "X-B"} {
		if _, err := db.GetOption(key); err == nil || adbcStatus(t, err) != adbc.StatusNotFound {
			t.Errorf("GetOption(%q) must not hand back a secret, got %v", key, err)
		}
	}
}

func TestDatabaseDeprecationWarningOnceWithoutValues(t *testing.T) {
	out := captureWarnings(t)
	const secret = "value-that-must-not-print"
	db := newTestDatabase(t, map[string]string{"adbc.quack.token": secret, "adbc.quack.tls": "false"})
	if err := db.SetOption("adbc.quack.token", secret+"-2"); err != nil {
		t.Fatal(err)
	}
	_, _ = db.GetOption("adbc.quack.tls")
	_, _ = db.GetOption("adbc.quack.token")
	newTestDatabase(t, map[string]string{"adbc.quack.tls": "true"})
	// The current names never warn.
	newTestDatabase(t, map[string]string{OptionToken: secret, OptionTLS: "true"})

	got := out.String()
	want := `[quack] option "adbc.quack.token" is deprecated; use "quack.auth.token"` + "\n" +
		`[quack] option "adbc.quack.tls" is deprecated; use "quack.tls.enabled"` + "\n"
	lines := strings.SplitAfter(got, "\n")
	if len(lines) != 3 || lines[2] != "" || !strings.Contains(want, lines[0]) || !strings.Contains(want, lines[1]) || lines[0] == lines[1] {
		t.Fatalf("want each warning once:\n%s\ngot:\n%s", want, got)
	}
	if strings.Contains(got, secret) || strings.Contains(got, "false") {
		t.Fatalf("a warning carries an option value:\n%s", got)
	}
}

func TestUnknownOptionsAreRefusedWithAPrefixedMessage(t *testing.T) {
	captureWarnings(t)
	for _, key := range []string{"quack.nope", "quack.auth.tokn", "adbc.nope", "nope"} {
		_, err := NewDriver(nil).NewDatabase(map[string]string{key: "x"})
		if err == nil {
			t.Errorf("%q: want NOT_IMPLEMENTED, got success", key)
			continue
		}
		if code := adbcStatus(t, err); code != adbc.StatusNotImplemented {
			t.Errorf("%q: status %v, want NOT_IMPLEMENTED", key, code)
		}
	}
	// Accepted as before 1.1 (every adbc.quack.* key was), and spec keys.
	for _, key := range []string{"adbc.quack.something_else", OptionURI, adbc.OptionKeyUsername, adbc.OptionKeyPassword, "adbc.telemetry.trace_parent"} {
		if _, err := NewDriver(nil).NewDatabase(map[string]string{key: "x"}); err != nil {
			t.Errorf("%q: %v", key, err)
		}
	}
	db := newTestDatabase(t, nil)
	if _, err := db.Open(t.Context()); err == nil || adbcStatus(t, err) != adbc.StatusInvalidArgument ||
		!strings.Contains(err.Error(), `missing required option "uri"`) {
		t.Errorf("Open without a uri: %v", err)
	}
	if _, err := db.GetOption("quack.nope"); err == nil || adbcStatus(t, err) != adbc.StatusNotFound {
		t.Errorf("GetOption of an unset key: %v", err)
	}
}

func TestTransportErrorsCarryThePrefixOnce(t *testing.T) {
	err := fromTransportError(errors.New("transport: dial tcp: refused"))
	if err.Error() == "" || !strings.HasPrefix(err.(adbc.Error).Msg, "[quack] transport:") {
		t.Fatalf("got %q", err.(adbc.Error).Msg)
	}
	again := fromTransportError(err)
	if strings.Count(again.(adbc.Error).Msg, "[quack]") != 1 {
		t.Fatalf("prefix added twice: %q", again.(adbc.Error).Msg)
	}
}

// gizmodata/adbc-install's weekly canary recognizes this driver by the text
// "(tried http://127.0.0.1:1/quack)" in its connection error: keep it verbatim
// (a prefix is fine).
func TestRefusedConnectionErrorNamesTheURLItTried(t *testing.T) {
	db := newTestDatabase(t, map[string]string{OptionURI: "quack://127.0.0.1:1"})
	_, err := db.Open(t.Context())
	if err == nil {
		t.Fatal("connected to 127.0.0.1:1, where nothing should listen")
	}
	adbcStatus(t, err) // and the [quack] prefix
	if msg := err.Error(); !strings.Contains(msg, "(tried http://127.0.0.1:1/quack)") {
		t.Fatalf("the error no longer names the URL it tried: %q", msg)
	}
}

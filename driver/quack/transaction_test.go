package quack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"

	"github.com/gizmodata/adbc-driver-quack/internal/message"
)

// fakeQuackServer is an in-process Quack server good enough for
// connection/transaction lifecycle tests: it answers the handshake and
// records every SQL statement prepared against it, returning an empty
// result for each.
type fakeQuackServer struct {
	mu  sync.Mutex
	sql []string
}

func (f *fakeQuackServer) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sql...)
}

func (f *fakeQuackServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server: read body: %v", err)
			return
		}
		req, err := message.DecodeMessage(body)
		if err != nil {
			t.Errorf("server: decode: %v", err)
			return
		}
		var resp message.QuackMessage
		switch m := req.(type) {
		case message.ConnectionRequest:
			resp = message.ConnectionResponse{
				Hdr: message.MessageHeader{
					Type:         message.MessageTypeConnectionResponse,
					ConnectionID: "fake-conn-1",
				},
				ServerDuckDBVersion: "fake",
				ServerPlatform:      "test",
			}
		case message.PrepareRequest:
			f.mu.Lock()
			f.sql = append(f.sql, m.SQL)
			f.mu.Unlock()
			resp = message.PrepareResponse{
				Hdr: message.MessageHeader{
					Type:         message.MessageTypePrepareResponse,
					ConnectionID: "fake-conn-1",
				},
			}
		case message.DisconnectMessage:
			resp = message.SuccessResponse{
				Hdr: message.MessageHeader{Type: message.MessageTypeSuccessResponse},
			}
		default:
			t.Errorf("server: unexpected message %T", req)
			return
		}
		encoded, err := message.EncodeMessage(resp)
		if err != nil {
			t.Errorf("server: encode: %v", err)
			return
		}
		_, _ = w.Write(encoded)
	}
}

// openFakeConnection starts a fake server and opens a driver connection
// against it. Cleanup is registered on t.
func openFakeConnection(t *testing.T) (*fakeQuackServer, adbc.Connection) {
	t.Helper()
	srv := &fakeQuackServer{}
	hs := httptest.NewServer(srv.handler(t))
	t.Cleanup(hs.Close)

	uri := "quack://" + strings.TrimPrefix(hs.URL, "http://")
	db, err := NewDriver(nil).NewDatabase(map[string]string{OptionURI: uri})
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	conn, err := db.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return srv, conn
}

func execSQL(t *testing.T, conn adbc.Connection, sql string) {
	t.Helper()
	stmt, err := conn.NewStatement()
	if err != nil {
		t.Fatalf("NewStatement: %v", err)
	}
	defer stmt.Close()
	if err := stmt.SetSqlQuery(sql); err != nil {
		t.Fatalf("SetSqlQuery: %v", err)
	}
	if _, err := stmt.ExecuteUpdate(context.Background()); err != nil {
		t.Fatalf("ExecuteUpdate(%s): %v", sql, err)
	}
}

func setAutocommit(t *testing.T, conn adbc.Connection, enabled bool) {
	t.Helper()
	value := adbc.OptionValueEnabled
	if !enabled {
		value = adbc.OptionValueDisabled
	}
	if err := conn.(adbc.PostInitOptions).SetOption(adbc.OptionKeyAutoCommit, value); err != nil {
		t.Fatalf("SetOption(autocommit=%v): %v", enabled, err)
	}
}

func TestManualCommitLazyBegin(t *testing.T) {
	srv, conn := openFakeConnection(t)
	ctx := context.Background()

	setAutocommit(t, conn, false)
	if got := srv.statements(); len(got) != 0 {
		t.Fatalf("disabling autocommit must not run SQL, server saw %v", got)
	}

	// Commit with nothing pending is a no-op, not a server round-trip.
	if err := conn.Commit(ctx); err != nil {
		t.Fatalf("empty Commit: %v", err)
	}
	if got := srv.statements(); len(got) != 0 {
		t.Fatalf("empty commit must be a no-op, server saw %v", got)
	}

	execSQL(t, conn, "INSERT INTO t VALUES (1)")
	execSQL(t, conn, "INSERT INTO t VALUES (2)")
	if err := conn.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	want := []string{
		"BEGIN TRANSACTION",
		"INSERT INTO t VALUES (1)",
		"INSERT INTO t VALUES (2)",
		"COMMIT",
	}
	got := srv.statements()
	if len(got) != len(want) {
		t.Fatalf("statements: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("statement[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestManualRollbackThenLazyReBegin(t *testing.T) {
	srv, conn := openFakeConnection(t)
	ctx := context.Background()

	setAutocommit(t, conn, false)
	execSQL(t, conn, "INSERT INTO t VALUES (1)")
	if err := conn.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// Next statement after rollback re-opens a transaction lazily.
	execSQL(t, conn, "INSERT INTO t VALUES (2)")
	if err := conn.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	want := []string{
		"BEGIN TRANSACTION",
		"INSERT INTO t VALUES (1)",
		"ROLLBACK",
		"BEGIN TRANSACTION",
		"INSERT INTO t VALUES (2)",
		"COMMIT",
	}
	got := srv.statements()
	if len(got) != len(want) {
		t.Fatalf("statements: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("statement[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestReenableAutocommitCommitsPendingTx(t *testing.T) {
	srv, conn := openFakeConnection(t)

	setAutocommit(t, conn, false)
	execSQL(t, conn, "INSERT INTO t VALUES (1)")
	setAutocommit(t, conn, true)

	got := srv.statements()
	want := []string{"BEGIN TRANSACTION", "INSERT INTO t VALUES (1)", "COMMIT"}
	if len(got) != len(want) {
		t.Fatalf("statements: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("statement[%d]: got %q want %q", i, got[i], want[i])
		}
	}

	// Back in autocommit: statements run bare.
	execSQL(t, conn, "INSERT INTO t VALUES (2)")
	if got := srv.statements(); got[len(got)-1] != "INSERT INTO t VALUES (2)" {
		t.Errorf("autocommit statement: got %q", got[len(got)-1])
	}
}

func TestAutocommitNeverBegins(t *testing.T) {
	srv, conn := openFakeConnection(t)

	execSQL(t, conn, "SELECT 1")
	want := []string{"SELECT 1"}
	got := srv.statements()
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("statements: got %v want %v", got, want)
	}
	if err := conn.Commit(context.Background()); err == nil {
		t.Error("Commit in autocommit mode should error")
	}
}

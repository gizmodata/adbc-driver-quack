package quack

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gizmodata/adbc-driver-quack/internal/transport"
)

// liveURI is the Quack server for live tests (see validation_test.go).
func liveURI(t *testing.T, opts map[string]string) transport.QuackURI {
	t.Helper()
	uri := os.Getenv("ADBC_QUACK_TEST_URI")
	if uri == "" {
		if os.Getenv("ADBC_QUACK_TEST_REQUIRE") != "" {
			t.Fatal("ADBC_QUACK_TEST_REQUIRE is set but ADBC_QUACK_TEST_URI is not")
		}
		t.Skip("ADBC_QUACK_TEST_URI not set")
	}
	token := os.Getenv("ADBC_QUACK_TEST_TOKEN")
	if token == "" {
		token = "adbc-driver-quack-it-token"
	}
	all := map[string]string{"adbc.quack.token": token}
	for k, v := range opts {
		all[k] = v
	}
	u, err := transport.ParseURI(uri, all)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestHeartbeatKeepsAnIdleConnectionAlive: with a 2 s lease, a connection
// left idle for 5 s still works because the heartbeat renews it, and the
// negative twin (heartbeat stopped) is refused once the lease runs out, so
// the positive result is the heartbeat's doing.
func TestHeartbeatKeepsAnIdleConnectionAlive(t *testing.T) {
	u := liveURI(t, map[string]string{"adbc.quack.heartbeat_timeout_seconds": "2"})
	ctx := context.Background()

	alive, err := newSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer alive.close(ctx)
	if alive.lease != 2*time.Second {
		t.Fatalf("server accepted a %v lease, want 2s", alive.lease)
	}

	idle, err := newSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.close(ctx)
	idle.stopHeartbeat()

	time.Sleep(5 * time.Second)

	if _, err := alive.drainPrepared(ctx, "SELECT 1"); err != nil {
		t.Fatalf("connection with heartbeat: %v", err)
	}
	_, err = idle.drainPrepared(ctx, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "lease expired") {
		t.Fatalf("connection without heartbeat: want a lease-expired error, got %v", err)
	}
}

// TestLargeResultStreamsInBatches drives many FETCH batches (indices,
// acks, the terminal total) and checks every row arrives once. The rows are
// wide enough (~200 MB) that the server's 32 MiB batches split them up.
func TestLargeResultStreamsInBatches(t *testing.T) {
	u := liveURI(t, nil)
	ctx := context.Background()
	s, err := newSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close(ctx)
	cur, err := s.cursor(ctx, "SELECT i, repeat('x', 64) || i AS label FROM range(3000000) t(i)")
	if err != nil {
		t.Fatal(err)
	}
	defer cur.close()
	var rows, sum int64
	for {
		ch, err := cur.nextChunk(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ch == nil {
			break
		}
		for r := 0; r < ch.RowCount; r++ {
			sum += ch.Columns[0].GetObject(r).(int64)
		}
		rows += int64(ch.RowCount)
	}
	if rows != 3000000 || sum != 3000000*2999999/2 {
		t.Fatalf("got %d rows summing to %d", rows, sum)
	}
	if cur.nextBatch < 3 {
		t.Fatalf("expected several FETCH batches, got %d", cur.nextBatch-1)
	}
}

// TestStatementAfterCancelWaitsForTheCancel: the server-side CANCEL sent for
// an abandoned statement must reach the server before the caller's next
// statement does; a late CANCEL would interrupt that next statement (the CI
// failure behind 1.0.0's first tag run). The hook delays the CANCEL by a
// second, so without the ordering the follow-up query would be sent first.
func TestStatementAfterCancelWaitsForTheCancel(t *testing.T) {
	u := liveURI(t, nil)
	ctx := context.Background()
	s, err := newSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close(ctx)
	testCancelDelay = time.Second
	defer func() { testCancelDelay = 0 }()

	qctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_, err = s.drainPrepared(qctx, "SELECT count(*) FROM range(100000000000) a")
	cancel()
	if err == nil {
		t.Fatal("the long query should have been cancelled")
	}
	result, err := s.drainPrepared(ctx, "SELECT 42")
	sent := time.Now().UnixNano()
	if err != nil {
		t.Fatalf("follow-up query: %v", err)
	}
	if v := result.Chunks[0].Columns[0].GetObject(0); v != int32(42) {
		t.Fatalf("follow-up query returned %v", v)
	}
	done := s.lastCancelDone.Load()
	if done == 0 || done > sent {
		t.Fatalf("the follow-up statement finished before the CANCEL was answered (cancel done %d, query done %d)", done, sent)
	}
}

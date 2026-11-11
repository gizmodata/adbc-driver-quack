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

// TestListLongerThanTheUnbackedRowCapDecodes: the decoder refuses a vector of
// more than 2^20 rows unless the message holds its rows. A long list's child
// is such a vector; the real server sends it FLAT, so it must keep decoding.
func TestListLongerThanTheUnbackedRowCapDecodes(t *testing.T) {
	u := liveURI(t, nil)
	ctx := context.Background()
	s, err := newSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close(ctx)
	cur, err := s.cursor(ctx, "SELECT range(2000000) AS a")
	if err != nil {
		t.Fatal(err)
	}
	defer cur.close()
	ch, err := cur.nextChunk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ch == nil || ch.RowCount != 1 {
		t.Fatalf("want one row, got %+v", ch)
	}
	list, ok := ch.Columns[0].GetObject(0).([]interface{})
	if !ok || len(list) != 2000000 {
		t.Fatalf("want a 2000000-element list, got %T of %d", ch.Columns[0].GetObject(0), len(list))
	}
	if list[0] != int64(0) || list[1999999] != int64(1999999) {
		t.Fatalf("first %v, last %v", list[0], list[1999999])
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

// TestSecondQueryKeepsTheOpenResult: the Quack server streams one result per
// connection, and a new PREPARE supersedes an unfinished one. A result the
// caller is still reading must survive a second query on the same
// connection (two cursors, a metadata call in the middle of a read), so the
// session reads the rest of the open result into memory before the new
// query starts. The first result is several FETCH batches long, so most of
// it is still on the server when the second query is sent.
func TestSecondQueryKeepsTheOpenResult(t *testing.T) {
	u := liveURI(t, nil)
	ctx := context.Background()
	s, err := newSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close(ctx)

	const n = 3000000
	first, err := s.cursor(ctx, "SELECT i, repeat('x', 64) || i AS label FROM range(3000000) t(i)")
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	if !first.needsMore {
		t.Fatal("the first result fit in PREPARE's inline rows; the test needs it to stream")
	}
	var rows, sum int64
	readChunk := func() bool {
		ch, err := first.nextChunk(ctx)
		if err != nil {
			t.Fatalf("reading the first result: %v", err)
		}
		if ch == nil {
			return false
		}
		for r := 0; r < ch.RowCount; r++ {
			sum += ch.Columns[0].GetObject(r).(int64)
		}
		rows += int64(ch.RowCount)
		return true
	}
	readChunk()

	second, err := s.drainPrepared(ctx, "SELECT 42")
	if err != nil {
		t.Fatalf("second query: %v", err)
	}
	if v := second.Chunks[0].Columns[0].GetObject(0); v != int32(42) {
		t.Fatalf("second query returned %v", v)
	}

	for readChunk() {
	}
	if rows != n || sum != n*(n-1)/2 {
		t.Fatalf("first result after a second query: %d rows summing to %d, want %d rows", rows, sum, n)
	}
}

// TestOpenResultBufferingFailureReachesItsReader: when reading the rest of
// the open result fails (here the second query's context is already
// cancelled), the first cursor's next read must return that error, never
// end early as if the result were complete.
func TestOpenResultBufferingFailureReachesItsReader(t *testing.T) {
	u := liveURI(t, nil)
	ctx := context.Background()
	s, err := newSession(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close(ctx)

	first, err := s.cursor(ctx, "SELECT i, repeat('x', 64) || i AS label FROM range(3000000) t(i)")
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	if !first.needsMore {
		t.Fatal("the first result fit in PREPARE's inline rows; the test needs it to stream")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.drainPrepared(cancelled, "SELECT 42"); err == nil {
		t.Fatal("a query with a cancelled context should fail")
	}

	for {
		ch, err := first.nextChunk(ctx)
		if err != nil {
			if !strings.Contains(err.Error(), "reading the rest of the result before a new statement") {
				t.Fatalf("unexpected error: %v", err)
			}
			break
		}
		if ch == nil {
			t.Fatal("the first result ended as if complete after its buffering failed")
		}
	}

	// The connection is still usable.
	result, err := s.drainPrepared(ctx, "SELECT 42")
	if err != nil {
		t.Fatalf("follow-up query: %v", err)
	}
	if v := result.Chunks[0].Columns[0].GetObject(0); v != int32(42) {
		t.Fatalf("follow-up query returned %v", v)
	}
}

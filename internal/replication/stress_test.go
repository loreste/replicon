package replication

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Race condition tests — run with go test -race
// ---------------------------------------------------------------------------

// TestServiceConcurrentRecord verifies that concurrent calls to record() do
// not produce data races or corrupt the audit log.
func TestServiceConcurrentRecord(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	service := NewService(auditPath)

	const workers = 20
	const perWorker = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(id int) {
			defer wg.Done()
			for i := range perWorker {
				result := CommandResult{
					Action:    fmt.Sprintf("action-%d", id),
					Status:    "ok",
					Summary:   fmt.Sprintf("worker %d iteration %d", id, i),
					StartedAt: time.Now().UTC(),
				}
				result.Finalize()
				service.record(result)
			}
		}(w)
	}
	wg.Wait()

	// Verify the audit log has exactly workers*perWorker entries.
	f, err := os.Open(auditPath)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()

	results, err := decodeAuditLog(f)
	if err != nil {
		t.Fatalf("decode audit log: %v", err)
	}
	if len(results) != workers*perWorker {
		t.Fatalf("expected %d entries, got %d", workers*perWorker, len(results))
	}
}

// TestMetricsConcurrentObserve verifies that concurrent Observe calls on the
// Metrics struct do not race.
func TestMetricsConcurrentObserve(t *testing.T) {
	m := NewMetrics()
	const workers = 20
	const perWorker = 100

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(id int) {
			defer wg.Done()
			action := fmt.Sprintf("action-%d", id%5)
			for range perWorker {
				m.Observe(action, "ok", 10)
			}
		}(w)
	}
	wg.Wait()

	output := m.RenderPrometheus()
	if !strings.Contains(output, "replicon_command_runs_total") {
		t.Fatal("expected metrics output")
	}
}

// TestMetricsConcurrentRender verifies that rendering Prometheus output while
// recording new observations does not race.
func TestMetricsConcurrentRender(t *testing.T) {
	m := NewMetrics()
	done := make(chan struct{})

	// Writer goroutine.
	go func() {
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
				m.Observe("verify", "ok", int64(i%100))
			}
		}
	}()

	// Reader goroutine.
	for range 50 {
		output := m.RenderPrometheus()
		if output == "" {
			t.Fatal("empty metrics output")
		}
	}

	close(done)
}

// ---------------------------------------------------------------------------
// Goroutine leak detection
// ---------------------------------------------------------------------------

// TestNoGoroutineLeakOnValidate ensures that validate does not leak
// goroutines even when called many times in a loop.
func TestNoGoroutineLeakOnValidate(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "replicon.json")
	sample, _ := SampleConfig("master-slave")
	if err := os.WriteFile(configPath, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	// Let the runtime settle before counting goroutines.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	service := NewService("")
	for range 100 {
		// Validate will fail on DSN resolution but that is fine —
		// we are testing that no goroutines leak on each call.
		_, _ = service.ValidateConfigFile(configPath)
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()

	// Allow a small margin (runtime goroutines can fluctuate).
	if after > before+5 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

// TestNoGoroutineLeakOnVerifyMissingDSN ensures verify does not leak
// goroutines when the DSN is unavailable.
func TestNoGoroutineLeakOnVerifyMissingDSN(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	cfg := Config{
		ClusterName:     "leak-test",
		Mode:            "master-slave",
		ReplicationUser: "repl",
		ReplicationSlot: "slot",
		Primary: NodeConfig{
			Name: "p", Host: "10.0.0.1", Port: 5432,
			DataDir: "/d", PostgresUser: "pg", SSHUser: "u", ServerID: "a",
			DSNEnv: "REPLICON_NONEXISTENT_LEAK_TEST_PRIMARY",
		},
		Standby: NodeConfig{
			Name: "s", Host: "10.0.0.2", Port: 5432,
			DataDir: "/d", PostgresUser: "pg", SSHUser: "u", ServerID: "b",
			DSNEnv: "REPLICON_NONEXISTENT_LEAK_TEST_STANDBY",
		},
		Network: Network{ReplicationCIDR: "10.0.0.2/32", ApplicationName: "test"},
	}

	for range 50 {
		_, _ = VerifyReplication(cfg)
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()

	if after > before+5 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

// ---------------------------------------------------------------------------
// Memory / allocation benchmarks
// ---------------------------------------------------------------------------

// BenchmarkSanitizeResult measures allocations when sanitizing a result with
// nested details, which is called on every API response.
func BenchmarkSanitizeResult(b *testing.B) {
	result := CommandResult{
		Action:  "verify",
		Cluster: "orders-prod",
		Mode:    "master-slave",
		Status:  "ok",
		Summary: "password=secret postgres://user:pass@host:5432/db",
		Error:   "",
		Details: map[string]any{
			"primary": map[string]any{
				"name":             "primary",
				"application_name": "orders-standby",
				"client_addr":      "10.0.0.11/32",
				"state":            "streaming",
				"sync_state":       "async",
			},
			"standby": map[string]any{
				"name":                 "standby",
				"in_recovery":          true,
				"receive_lsn":          "0/3000148",
				"replay_lsn":           "0/3000148",
				"replay_delay_seconds": 0.0,
			},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = result.Sanitized()
	}
}

// BenchmarkRenderPrometheus measures allocations when rendering Prometheus
// metrics, which is called on every /metrics request.
func BenchmarkRenderPrometheus(b *testing.B) {
	m := NewMetrics()
	for _, action := range []string{"validate", "verify", "probe", "promote", "rejoin"} {
		for _, status := range []string{"ok", "error"} {
			m.Observe(action, status, 100)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = m.RenderPrometheus()
	}
}

// BenchmarkRedactConnectionError measures the cost of redacting connection
// errors using the pre-compiled regexes.
func BenchmarkRedactConnectionError(b *testing.B) {
	err := fmt.Errorf("failed to connect to `host=10.0.0.10 user=postgres password=secret database=postgres`: dial tcp 10.0.0.10:5432: connect: connection refused, also tried postgres://user:pass@host/db")

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = redactConnectionError(err)
	}
}

// ---------------------------------------------------------------------------
// Ring buffer audit log correctness
// ---------------------------------------------------------------------------

// TestDecodeTailAuditLogExact verifies the ring buffer returns exactly the
// last N entries when the log has more than N entries.
func TestDecodeTailAuditLogExact(t *testing.T) {
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf(
			`{"action":"a%d","status":"ok","summary":"s","started_at":"2026-05-04T12:%02d:00Z","finished_at":"2026-05-04T12:%02d:01Z","duration_ms":1}`,
			i, i%60, i%60,
		))
	}
	input := strings.Join(lines, "\n") + "\n"

	results, err := decodeTailAuditLog(strings.NewReader(input), 10)
	if err != nil {
		t.Fatalf("decodeTailAuditLog: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10, got %d", len(results))
	}
	// Should be the last 10 entries (a90..a99).
	for i, r := range results {
		expected := fmt.Sprintf("a%d", 90+i)
		if r.Action != expected {
			t.Fatalf("entry %d: expected action %q, got %q", i, expected, r.Action)
		}
	}
}

// TestDecodeTailAuditLogFewerThanLimit verifies that requesting more entries
// than exist returns all entries.
func TestDecodeTailAuditLogFewerThanLimit(t *testing.T) {
	input := `{"action":"first","status":"ok","summary":"s","started_at":"2026-05-04T12:00:00Z","finished_at":"2026-05-04T12:00:01Z","duration_ms":1}
{"action":"second","status":"ok","summary":"s","started_at":"2026-05-04T12:01:00Z","finished_at":"2026-05-04T12:01:01Z","duration_ms":1}
`
	results, err := decodeTailAuditLog(strings.NewReader(input), 50)
	if err != nil {
		t.Fatalf("decodeTailAuditLog: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2, got %d", len(results))
	}
	if results[0].Action != "first" || results[1].Action != "second" {
		t.Fatalf("unexpected order: %v", results)
	}
}

// TestDecodeTailAuditLogEmpty verifies that an empty log returns no entries.
func TestDecodeTailAuditLogEmpty(t *testing.T) {
	results, err := decodeTailAuditLog(strings.NewReader(""), 10)
	if err != nil {
		t.Fatalf("decodeTailAuditLog: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Audit log stress under concurrent writes and reads
// ---------------------------------------------------------------------------

// TestAuditLogConcurrentWriteAndRead simulates the service mode pattern where
// API handlers record results while the history endpoint reads them.
func TestAuditLogConcurrentWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	service := NewService(auditPath)

	const iterations = 200
	var wg sync.WaitGroup

	// Writer goroutines.
	wg.Add(3)
	for w := range 3 {
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				result := CommandResult{
					Action:    "verify",
					Status:    "ok",
					Summary:   fmt.Sprintf("w%d-i%d", id, i),
					StartedAt: time.Now().UTC(),
				}
				result.Finalize()
				service.record(result)
			}
		}(w)
	}

	// Reader goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iterations {
			_, _ = service.History(10)
			runtime.Gosched()
		}
	}()

	wg.Wait()

	// Final read should succeed.
	results, err := service.History(10)
	if err != nil {
		t.Fatalf("final history read: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected non-empty history")
	}
}

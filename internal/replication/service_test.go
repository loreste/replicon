package replication

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeAuditLog(t *testing.T) {
	input := `{"action":"verify","status":"ok","summary":"done","started_at":"2026-05-04T12:00:00Z","finished_at":"2026-05-04T12:00:01Z","duration_ms":1000}
{"action":"probe","status":"error","summary":"failed","started_at":"2026-05-04T12:01:00Z","finished_at":"2026-05-04T12:01:01Z","duration_ms":1000}
`

	results, err := decodeAuditLog(strings.NewReader(input))
	if err != nil {
		t.Fatalf("decode audit log: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Action != "verify" || results[1].Action != "probe" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestHistoryLimit(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	content := `{"action":"validate","status":"ok","summary":"one","started_at":"2026-05-04T12:00:00Z","finished_at":"2026-05-04T12:00:01Z","duration_ms":1000}
{"action":"verify","status":"ok","summary":"two","started_at":"2026-05-04T12:01:00Z","finished_at":"2026-05-04T12:01:01Z","duration_ms":1000}
{"action":"probe","status":"ok","summary":"three","started_at":"2026-05-04T12:02:00Z","finished_at":"2026-05-04T12:02:01Z","duration_ms":1000}
`
	if err := os.WriteFile(auditPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write audit log: %v", err)
	}

	service := NewService(auditPath)
	results, err := service.History(2)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Action != "verify" || results[1].Action != "probe" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

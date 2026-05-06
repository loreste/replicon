package replication

import "testing"

func TestCommandResultSanitized(t *testing.T) {
	result := CommandResult{
		Summary: "postgres://user:pass@db.internal:5432/postgres failed",
		Error:   "password=supersecret refused",
		Details: map[string]any{
			"dsn":      "postgres://user:pass@db.internal:5432/postgres",
			"password": "supersecret",
			"nested": map[string]any{
				"note": "safe",
			},
		},
	}

	sanitized := result.Sanitized()
	if sanitized.Details["dsn"] != "[redacted]" {
		t.Fatalf("expected dsn to be redacted: %#v", sanitized.Details)
	}
	if sanitized.Details["password"] != "[redacted]" {
		t.Fatalf("expected password to be redacted: %#v", sanitized.Details)
	}
	if sanitized.Error == result.Error {
		t.Fatalf("expected error to be sanitized: %q", sanitized.Error)
	}
}

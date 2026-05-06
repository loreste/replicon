package replication

import (
	"testing"
)

func TestVerifyReplicationUnsupportedMode(t *testing.T) {
	cfg := testConfig()
	cfg.Mode = "invalid-mode"

	result, err := VerifyReplication(cfg)
	if err == nil {
		t.Fatal("expected error for unsupported mode")
	}
	if result.Status != "error" {
		t.Fatalf("expected status error, got %q", result.Status)
	}
	if result.Action != "verify" {
		t.Fatalf("expected action verify, got %q", result.Action)
	}
}

func TestVerifyReplicationMasterSlaveMissingDSN(t *testing.T) {
	cfg := testConfig()
	cfg.Mode = "master-slave"
	cfg.Primary.DSN = ""
	cfg.Primary.DSNEnv = ""

	result, err := VerifyReplication(cfg)
	if err == nil {
		t.Fatal("expected error for missing primary DSN")
	}
	if result.Status != "error" {
		t.Fatalf("expected status error, got %q", result.Status)
	}
}

func TestVerifyReplicationMasterMasterMissingDSN(t *testing.T) {
	cfg := testConfig()
	cfg.Mode = "master-master"
	cfg.NodeA.DSN = ""
	cfg.NodeA.DSNEnv = ""

	result, err := VerifyReplication(cfg)
	if err == nil {
		t.Fatal("expected error for missing node-a DSN")
	}
	if result.Status != "error" {
		t.Fatalf("expected status error, got %q", result.Status)
	}
}

func TestRedactConnectionError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "redact_uri",
			input: "failed to connect to postgres://user:pass@host:5432/db timeout",
			want:  "failed to connect to [redacted-dsn] timeout",
		},
		{
			name:  "redact_password_param",
			input: "connection failed password=supersecret host=10.0.0.1",
			want:  "connection failed password=[redacted] host=10.0.0.1",
		},
		{
			name:  "no_sensitive_data",
			input: "connection refused",
			want:  "connection refused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactConnectionError(makeError(tt.input))
			if got != tt.want {
				t.Fatalf("redactConnectionError(%q)\n got: %q\nwant: %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewCommandResultVerify(t *testing.T) {
	cfg := testConfig()
	result := NewCommandResult("verify", cfg)
	if result.Action != "verify" {
		t.Fatalf("expected action verify, got %q", result.Action)
	}
	if result.Cluster != cfg.ClusterName {
		t.Fatalf("expected cluster %q, got %q", cfg.ClusterName, result.Cluster)
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string { return e.msg }

func makeError(msg string) error {
	return &testError{msg: msg}
}

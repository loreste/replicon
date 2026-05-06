package replication

import (
	"os"
	"testing"
)

func TestResolveDSNFromEnv(t *testing.T) {
	t.Setenv("REPLICON_TEST_DSN", "postgres://postgres:secret@127.0.0.1:5432/postgres?sslmode=disable")

	node := NodeConfig{DSNEnv: "REPLICON_TEST_DSN"}
	got, err := node.ResolveDSN()
	if err != nil {
		t.Fatalf("resolve dsn: %v", err)
	}
	if got == "" {
		t.Fatal("expected dsn from env")
	}
}

func TestResolveDSNMissingEnv(t *testing.T) {
	_ = os.Unsetenv("REPLICON_MISSING_DSN")

	node := NodeConfig{DSNEnv: "REPLICON_MISSING_DSN"}
	if _, err := node.ResolveDSN(); err == nil {
		t.Fatal("expected error for missing env dsn")
	}
}

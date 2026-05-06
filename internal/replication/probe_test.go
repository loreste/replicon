package replication

import (
	"strings"
	"testing"
)

func TestSanitizeProbeSegment(t *testing.T) {
	if got := sanitizeProbeSegment(" Node A "); got != "node-a" {
		t.Fatalf("unexpected sanitized value: %q", got)
	}
}

func TestNewProbeID(t *testing.T) {
	got := newProbeID("Primary A", "Standby B")
	if got == "" {
		t.Fatal("expected probe id")
	}
	if !strings.HasPrefix(got, "replicon-primary-a-to-standby-b-") {
		t.Fatalf("unexpected prefix: %q", got)
	}
}

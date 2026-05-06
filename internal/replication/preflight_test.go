package replication

import "testing"

func TestCheckSSHConnectivityUnsupportedMode(t *testing.T) {
	cfg := testConfig()
	cfg.Mode = "unknown"

	_, err := CheckSSHConnectivity(cfg, 0)
	if err == nil {
		t.Fatal("expected error for unsupported mode")
	}
}

func TestCheckSSHConnectivitySkipsEmptyHost(t *testing.T) {
	cfg := testConfig()
	cfg.Mode = "master-slave"
	cfg.Primary.Host = ""
	cfg.Standby.Host = ""

	results, err := CheckSSHConnectivity(cfg, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for empty hosts, got %d", len(results))
	}
}

func TestCheckNodeSSHUnreachable(t *testing.T) {
	node := NodeConfig{
		Name:    "unreachable",
		Host:    "192.0.2.1", // TEST-NET, guaranteed non-routable
		SSHUser: "testuser",
	}

	pr := checkNodeSSH(node, 2_000_000_000) // 2s timeout
	if pr.Reachable {
		t.Fatal("expected node to be unreachable")
	}
	if pr.Error == "" {
		t.Fatal("expected error message")
	}
	if pr.Node != "unreachable" {
		t.Fatalf("expected node name %q, got %q", "unreachable", pr.Node)
	}
}

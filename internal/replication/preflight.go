package replication

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// PreflightResult holds the results of SSH connectivity checks.
type PreflightResult struct {
	Node    string `json:"node"`
	Host    string `json:"host"`
	Reachable bool   `json:"reachable"`
	Error   string `json:"error,omitempty"`
}

// CheckSSHConnectivity verifies SSH access to the nodes that will be used
// by promote or rejoin operations. It runs a simple "true" command over SSH
// with a short timeout to validate connectivity before executing destructive
// operations.
func CheckSSHConnectivity(cfg Config, timeout time.Duration) ([]PreflightResult, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	var nodes []NodeConfig
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "master-slave":
		nodes = append([]NodeConfig{cfg.Primary}, cfg.ResolveStandbys()...)
	case "master-master":
		nodes = []NodeConfig{cfg.NodeA, cfg.NodeB}
	default:
		return nil, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}

	results := make([]PreflightResult, 0, len(nodes))
	var failures int

	for _, node := range nodes {
		if node.Host == "" {
			continue
		}
		pr := checkNodeSSH(node, timeout)
		results = append(results, pr)
		if !pr.Reachable {
			failures++
		}
	}

	if failures > 0 {
		return results, fmt.Errorf("ssh preflight failed: %d of %d nodes unreachable", failures, len(results))
	}
	return results, nil
}

func checkNodeSSH(node NodeConfig, timeout time.Duration) PreflightResult {
	pr := PreflightResult{
		Node: node.Name,
		Host: node.Host,
	}

	target := node.Host
	if strings.TrimSpace(node.SSHUser) != "" {
		target = fmt.Sprintf("%s@%s", node.SSHUser, node.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		target,
		"true",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" {
			text = err.Error()
		}
		pr.Error = sanitizeRemoteOutput(text)
		pr.Reachable = false
		return pr
	}

	pr.Reachable = true
	return pr
}

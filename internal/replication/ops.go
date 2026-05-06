package replication

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type RemoteCommand struct {
	Node    string `json:"node"`
	Command string `json:"command"`
}

type OperationOptions struct {
	Execute bool
	Timeout time.Duration
}

func PromoteStandby(cfg Config, opts OperationOptions) (CommandResult, error) {
	result := NewCommandResult("promote", cfg)
	if strings.ToLower(strings.TrimSpace(cfg.Mode)) != "master-slave" {
		result.Status = "error"
		result.Summary = "Promotion is only supported for master-slave mode"
		result.Error = result.Summary
		result.Finalize()
		return result, errors.New(result.Error)
	}

	target := selectPromotionTarget(cfg)

	commands := []RemoteCommand{
		{Node: target.Name, Command: `sudo -u postgres psql -c "SELECT pg_promote(wait_seconds => 60);"`},
		{Node: target.Name, Command: `sudo -u postgres psql -tAc "SELECT NOT pg_is_in_recovery();"`},
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}

	if err := runCommands(cfg, commands, opts); err != nil {
		result.Status = "error"
		result.Summary = "Standby promotion failed"
		result.Error = err.Error()
		result.Details = map[string]any{
			"commands":   commands,
			"old_role":   cfg.Primary.Name,
			"new_role":   target.Name,
			"executed":   opts.Execute,
			"next_steps": []string{"Fence the old primary before allowing writes", "Run rejoin once the old primary is safe to overwrite"},
		}
		result.Finalize()
		return result, err
	}

	result.Summary = fmt.Sprintf("Promote workflow ready: %s becomes primary", target.Name)
	if opts.Execute {
		result.Summary = fmt.Sprintf("Promotion completed: %s is now primary", target.Name)
	}
	result.Details = map[string]any{
		"commands":   commands,
		"old_role":   cfg.Primary.Name,
		"new_role":   target.Name,
		"executed":   opts.Execute,
		"next_steps": []string{"Fence or decommission the old primary", "Run rejoin to attach the old primary as a standby"},
	}
	result.Finalize()
	return result, nil
}

// selectPromotionTarget picks the best standby for promotion. In cluster mode,
// it queries each standby's replay LSN and picks the one closest to the
// primary. Falls back to the first standby if LSN queries fail.
func selectPromotionTarget(cfg Config) NodeConfig {
	standbys := cfg.ResolveStandbys()
	if len(standbys) <= 1 {
		return standbys[0]
	}

	type candidate struct {
		node       NodeConfig
		receiveLSN string
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var best candidate
	for _, s := range standbys {
		dsn, err := s.ResolveDSN()
		if err != nil {
			continue
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			continue
		}
		var lsn string
		err = conn.QueryRow(ctx, "SELECT COALESCE(pg_last_wal_receive_lsn()::text, '')").Scan(&lsn)
		conn.Close(ctx)
		if err != nil || lsn == "" {
			continue
		}
		// pg_lsn comparison: higher LSN = more data received.
		if best.receiveLSN == "" || lsn > best.receiveLSN {
			best = candidate{node: s, receiveLSN: lsn}
		}
	}

	if best.receiveLSN != "" {
		return best.node
	}
	return standbys[0]
}

func RejoinOldPrimary(cfg Config, opts OperationOptions) (CommandResult, error) {
	result := NewCommandResult("rejoin", cfg)
	if strings.ToLower(strings.TrimSpace(cfg.Mode)) != "master-slave" {
		result.Status = "error"
		result.Summary = "Rejoin is only supported for master-slave mode"
		result.Error = result.Summary
		result.Finalize()
		return result, errors.New(result.Error)
	}

	rejoinSlot := cfg.ReplicationSlot + "_rejoin"
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Minute
	}
	commands := []RemoteCommand{
		{Node: cfg.Standby.Name, Command: fmt.Sprintf(`sudo -u postgres psql -c %s`, shellQuote(fmt.Sprintf(`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = '%s') THEN PERFORM pg_create_physical_replication_slot('%s'); END IF; END $$;`, sqlLiteral(rejoinSlot), sqlLiteral(rejoinSlot))))},
		{Node: cfg.Primary.Name, Command: fmt.Sprintf(`sudo bash -lc 'data_dir=%s && backup_dir="${data_dir}.rejoin.$(date +%%Y%%m%%d-%%H%%M%%S)" && systemctl stop postgresql && while pgrep -x postgres >/dev/null; do sleep 1; done && if [ -d "$data_dir" ]; then mv "$data_dir" "$backup_dir"; fi && mkdir -p "$data_dir" && chown postgres:postgres "$data_dir" && chmod 700 "$data_dir"'`,
			shellQuote(cfg.Primary.DataDir),
		)},
		{Node: cfg.Primary.Name, Command: fmt.Sprintf(`sudo -u postgres pg_basebackup --pgdata=%s --write-recovery-conf --slot=%s --host=%s --port=%s --username=%s --checkpoint=fast --progress --no-password`,
			shellQuote(cfg.Primary.DataDir), shellQuote(rejoinSlot), shellQuote(cfg.Standby.Host), shellQuote(fmt.Sprintf("%d", cfg.Standby.Port)), shellQuote(cfg.ReplicationUser))},
		{Node: cfg.Primary.Name, Command: `sudo systemctl start postgresql`},
		{Node: cfg.Primary.Name, Command: `sudo -u postgres psql -tAc "SELECT pg_is_in_recovery();"`},
	}

	if err := runCommands(cfg, commands, opts); err != nil {
		result.Status = "error"
		result.Summary = "Rejoin workflow failed"
		result.Error = err.Error()
		result.Details = map[string]any{
			"commands": commands,
			"executed": opts.Execute,
			"notes":    []string{"Ensure .pgpass or another non-interactive credential source exists on the old primary host", "The old primary data directory is preserved under a timestamped backup path before rejoin"},
		}
		result.Finalize()
		return result, err
	}

	result.Summary = fmt.Sprintf("Rejoin workflow ready: %s will become standby of %s", cfg.Primary.Name, cfg.Standby.Name)
	if opts.Execute {
		result.Summary = fmt.Sprintf("Rejoin completed: %s is attached as standby to %s", cfg.Primary.Name, cfg.Standby.Name)
	}
	result.Details = map[string]any{
		"commands": commands,
		"executed": opts.Execute,
		"notes":    []string{"Ensure .pgpass or another non-interactive credential source exists on the old primary host", "The old primary data directory is preserved under a timestamped backup path before rejoin"},
	}
	result.Finalize()
	return result, nil
}

func runCommands(cfg Config, commands []RemoteCommand, opts OperationOptions) error {
	if !opts.Execute {
		return nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	for _, command := range commands {
		node, ok := lookupNode(cfg, command.Node)
		if !ok {
			return fmt.Errorf("unknown node %q", command.Node)
		}
		if err := executeRemoteCommand(node, command.Command, timeout); err != nil {
			return err
		}
	}
	return nil
}

func lookupNode(cfg Config, name string) (NodeConfig, bool) {
	for _, node := range []NodeConfig{cfg.Primary, cfg.Standby, cfg.NodeA, cfg.NodeB} {
		if node.Name == name {
			return node, true
		}
	}
	for _, node := range cfg.Standbys {
		if node.Name == name {
			return node, true
		}
	}
	return NodeConfig{}, false
}

func executeRemoteCommand(node NodeConfig, command string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	target := node.Host
	if strings.TrimSpace(node.SSHUser) != "" {
		target = fmt.Sprintf("%s@%s", node.SSHUser, node.Host)
	}

	cmd := exec.CommandContext(ctx, "ssh", target, command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" {
			text = err.Error()
		}
		return fmt.Errorf("remote command on %s failed: %s", node.Name, sanitizeRemoteOutput(text))
	}
	return nil
}

func sanitizeRemoteOutput(value string) string {
	value = strings.ReplaceAll(value, "postgres://", "[redacted-dsn]://")
	if strings.Contains(strings.ToLower(value), "password=") {
		return redactPasswordFragment(value)
	}
	return value
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, `'`, `'\''`) + "'"
}

func sqlLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

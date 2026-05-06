//go:build integration

package replication

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Skipf("skipping: %s not set", key)
	}
	return value
}

// integrationConfig builds a Config suitable for tests running against the
// Docker integration stack (primary on 55432, standby on 55433) or CI
// services (primary on 5432, standby on 5433).  The caller must ensure that
// the REPLICON_PRIMARY_DSN and REPLICON_STANDBY_DSN environment variables
// point at the correct instances.
func integrationConfig() Config {
	// Detect CI: GitHub Actions services expose 5432/5433 on localhost.
	primaryPort := 55432
	standbyPort := 55433
	primaryHost := "127.0.0.1"
	standbyHost := "127.0.0.2"
	if os.Getenv("CI") != "" {
		primaryPort = 5432
		standbyPort = 5433
		standbyHost = "127.0.0.1"
	}

	return Config{
		ClusterName:     "ci-test",
		Mode:            "master-slave",
		ReplicationUser: "replicator",
		ReplicationSlot: "replicon_demo_slot",
		Primary: NodeConfig{
			Name:         "ci-primary",
			Host:         primaryHost,
			Port:         primaryPort,
			DataDir:      "/var/lib/postgresql/data",
			PostgresUser: "postgres",
			SSHUser:      "ubuntu",
			ServerID:     "ci-a",
			DSNEnv:       "REPLICON_PRIMARY_DSN",
		},
		Standby: NodeConfig{
			Name:         "ci-standby",
			Host:         standbyHost,
			Port:         standbyPort,
			DataDir:      "/var/lib/postgresql/data",
			PostgresUser: "postgres",
			SSHUser:      "ubuntu",
			ServerID:     "ci-b",
			DSNEnv:       "REPLICON_STANDBY_DSN",
		},
		Network: Network{
			ReplicationCIDR: "0.0.0.0/0",
			ApplicationName: "orders-prod-standby",
		},
	}
}

// ---------------------------------------------------------------------------
// Connectivity
// ---------------------------------------------------------------------------

func TestIntegrationConnectPrimary(t *testing.T) {
	dsn := requireEnv(t, "REPLICON_PRIMARY_DSN")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect primary: %v", err)
	}
	defer conn.Close(ctx)

	var version string
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("query version: %v", err)
	}
	t.Logf("primary version: %s", version)
}

func TestIntegrationConnectStandby(t *testing.T) {
	dsn := requireEnv(t, "REPLICON_STANDBY_DSN")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect standby: %v", err)
	}
	defer conn.Close(ctx)

	var version string
	if err := conn.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("query version: %v", err)
	}
	t.Logf("standby version: %s", version)
}

// ---------------------------------------------------------------------------
// Config validation
// ---------------------------------------------------------------------------

func TestIntegrationValidateConfig(t *testing.T) {
	requireEnv(t, "REPLICON_PRIMARY_DSN")
	requireEnv(t, "REPLICON_STANDBY_DSN")

	cfg := integrationConfig()
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("validate config: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Probe table operations
// ---------------------------------------------------------------------------

func TestIntegrationProbeTableCreateAndCleanup(t *testing.T) {
	dsn := requireEnv(t, "REPLICON_PRIMARY_DSN")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Drop any stale probe table and create with the current schema
	_, _ = conn.Exec(ctx, `DROP TABLE IF EXISTS public.replicon_replication_probe`)
	if err := ensureProbeTable(ctx, conn); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Insert a probe row
	probeID := "integration-test-" + time.Now().Format("20060102-150405")
	if err := insertProbeRow(ctx, conn, probeID, "test"); err != nil {
		t.Fatalf("insert probe: %v", err)
	}

	// Verify it exists
	found, err := hasProbeRow(ctx, conn, probeID)
	if err != nil {
		t.Fatalf("query probe: %v", err)
	}
	if !found {
		t.Fatal("expected probe row to exist")
	}

	// Cleanup
	if err := deleteProbeRow(ctx, conn, probeID); err != nil {
		t.Fatalf("delete probe: %v", err)
	}

	// Drop table
	_, err = conn.Exec(ctx, `DROP TABLE IF EXISTS public.replicon_replication_probe`)
	if err != nil {
		t.Fatalf("drop table: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Live verify — queries pg_stat_replication and standby recovery state
// ---------------------------------------------------------------------------

func TestIntegrationVerifyReplication(t *testing.T) {
	requireEnv(t, "REPLICON_PRIMARY_DSN")
	requireEnv(t, "REPLICON_STANDBY_DSN")

	cfg := integrationConfig()
	result, err := VerifyReplication(cfg)
	if err != nil {
		t.Fatalf("verify replication: %v\nresult: %s", err, result.Text())
	}
	if result.Status != "ok" {
		t.Fatalf("expected status ok, got %q: %s", result.Status, result.Text())
	}
	t.Logf("verify output:\n%s", result.Text())

	// Check that details were populated.
	if result.Details == nil {
		t.Fatal("expected non-nil details")
	}
	primary, ok := result.Details["primary"].(map[string]any)
	if !ok {
		t.Fatal("expected primary details map")
	}
	if primary["state"] != "streaming" {
		t.Fatalf("expected primary state=streaming, got %v", primary["state"])
	}
	standby, ok := result.Details["standby"].(map[string]any)
	if !ok {
		t.Fatal("expected standby details map")
	}
	if standby["in_recovery"] != true {
		t.Fatalf("expected standby in_recovery=true, got %v", standby["in_recovery"])
	}
}

// ---------------------------------------------------------------------------
// Live probe — writes, replicates, deletes, confirms deletion replicates
// ---------------------------------------------------------------------------

func TestIntegrationProbeReplication(t *testing.T) {
	requireEnv(t, "REPLICON_PRIMARY_DSN")
	requireEnv(t, "REPLICON_STANDBY_DSN")

	cfg := integrationConfig()

	// Clean up any leftover probe table from a prior run.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	primaryDSN, _ := cfg.Primary.ResolveDSN()
	if conn, err := pgx.Connect(ctx, primaryDSN); err == nil {
		_, _ = conn.Exec(ctx, `DROP TABLE IF EXISTS public.replicon_replication_probe`)
		conn.Close(ctx)
	}

	result, err := ProbeReplication(cfg)
	if err != nil {
		t.Fatalf("probe replication: %v\nresult: %s", err, result.Text())
	}
	if result.Status != "ok" {
		t.Fatalf("expected status ok, got %q: %s", result.Status, result.Text())
	}
	t.Logf("probe output:\n%s", result.Text())

	// Check details.
	if result.Details == nil {
		t.Fatal("expected non-nil details")
	}
	if result.Details["probe_table"] != probeTableName {
		t.Fatalf("expected probe_table=%q, got %v", probeTableName, result.Details["probe_table"])
	}
}

// ---------------------------------------------------------------------------
// Live verify via Service layer (tests audit recording path end-to-end)
// ---------------------------------------------------------------------------

func TestIntegrationServiceVerify(t *testing.T) {
	requireEnv(t, "REPLICON_PRIMARY_DSN")
	requireEnv(t, "REPLICON_STANDBY_DSN")

	dir := t.TempDir()
	cfg := integrationConfig()

	// Write a temp config file.
	sample, _ := cfg.toJSON()
	configPath := dir + "/replicon.json"
	if err := os.WriteFile(configPath, sample, 0o644); err != nil {
		t.Fatal(err)
	}

	auditPath := dir + "/audit.jsonl"
	service := NewService(auditPath)

	result, err := service.VerifyConfigFile(configPath)
	if err != nil {
		t.Fatalf("service verify: %v\nresult: %s", err, result.Text())
	}
	if result.Status != "ok" {
		t.Fatalf("expected status ok, got %q: %s", result.Status, result.Text())
	}

	// Verify the audit log was written.
	entries, err := service.History(10)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Action != "verify" {
		t.Fatalf("expected verify action, got %q", entries[0].Action)
	}

	// Verify sanitization removes no sensitive data (DSN is in env, not result).
	sanitized := result.Sanitized()
	if sanitized.Status != "ok" {
		t.Fatalf("sanitized status should be ok, got %q", sanitized.Status)
	}
}

// ---------------------------------------------------------------------------
// Replication data flow — write on primary, confirm on standby
// ---------------------------------------------------------------------------

func TestIntegrationDataReplicates(t *testing.T) {
	primaryDSN := requireEnv(t, "REPLICON_PRIMARY_DSN")
	standbyDSN := requireEnv(t, "REPLICON_STANDBY_DSN")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	primary, err := pgx.Connect(ctx, primaryDSN)
	if err != nil {
		t.Fatalf("connect primary: %v", err)
	}
	defer primary.Close(ctx)

	standby, err := pgx.Connect(ctx, standbyDSN)
	if err != nil {
		t.Fatalf("connect standby: %v", err)
	}
	defer standby.Close(ctx)

	tableName := "replicon_data_flow_test"

	// Create a test table on the primary.
	_, err = primary.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.`+tableName+` (
		id serial PRIMARY KEY,
		value text NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now()
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer func() {
		_, _ = primary.Exec(context.Background(), `DROP TABLE IF EXISTS public.`+tableName)
	}()

	// Insert a row.
	sentinel := "replicon-data-test-" + time.Now().Format("20060102-150405.000")
	_, err = primary.Exec(ctx, `INSERT INTO public.`+tableName+` (value) VALUES ($1)`, sentinel)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Poll the standby until the row appears.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		var found bool
		err := standby.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM public.`+tableName+` WHERE value = $1)`,
			sentinel,
		).Scan(&found)

		if err != nil {
			// Table may not exist on standby yet — keep waiting.
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for row to replicate: %v", err)
			case <-ticker.C:
				continue
			}
		}

		if found {
			t.Logf("row %q replicated to standby", sentinel)
			break
		}

		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for row to replicate")
		case <-ticker.C:
		}
	}
}

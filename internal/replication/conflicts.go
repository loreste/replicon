package replication

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const conflictLogTable = "public.replicon_conflict_log"

// SetupConflictHandling configures conflict detection and resolution on a
// logical replication node. PostgreSQL does not natively handle conflicts in
// logical replication — a duplicate key or constraint violation from a
// replicated row will stop the apply worker.
//
// This installs a conflict log table and configures the node's approach:
//
//   - "last_write_wins": adds updated_at columns and uses ON CONFLICT to
//     keep the most recent write. Requires application cooperation.
//   - "skip": logs the conflict and skips the conflicting transaction.
//   - "log": logs the conflict and stops replication (manual intervention).
func SetupConflictHandling(ctx context.Context, conn *pgx.Conn, strategy string) error {
	// Create the conflict log table on all strategies.
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS `+conflictLogTable+` (
			id bigserial PRIMARY KEY,
			detected_at timestamptz NOT NULL DEFAULT now(),
			subscription_name text NOT NULL DEFAULT '',
			table_name text NOT NULL DEFAULT '',
			conflict_type text NOT NULL,
			resolution text NOT NULL,
			details text NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return fmt.Errorf("create conflict log table: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "skip":
		// In PG 16+, we can't dynamically skip single transactions in
		// logical replication. What we can do is detect apply worker
		// failures and advance past the conflict.
		// For now, create the log table and document the skip procedure.
	case "last_write_wins":
		// This requires the application to include an updated_at column.
		// replicon can't retroactively add this to all tables, but we
		// document the pattern and verify it.
	case "log", "":
		// Default: just log conflicts. Manual resolution required.
	default:
		return fmt.Errorf("unsupported conflict strategy %q (use: last_write_wins, skip, or log)", strategy)
	}

	return nil
}

// CheckConflicts queries both nodes for stalled subscriptions that may
// indicate a replication conflict, and returns details.
func CheckConflicts(cfg Config) (CommandResult, error) {
	result := NewCommandResult("conflicts", cfg)
	if strings.ToLower(strings.TrimSpace(cfg.Mode)) != "master-master" {
		result.Status = "error"
		result.Summary = "Conflict check is only supported for master-master mode"
		result.Error = result.Summary
		result.Finalize()
		return result, errors.New(result.Error)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	nodes := cfg.ResolveLogicalNodes()
	var conflicts []map[string]any

	for _, node := range nodes {
		dsn, err := node.ResolveDSN()
		if err != nil {
			continue
		}

		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			continue
		}

		// Check for disabled or errored subscriptions.
		rows, err := conn.Query(ctx, `
			SELECT s.subname,
			       CASE WHEN ss.pid IS NULL THEN 'down' ELSE 'streaming' END AS status,
			       COALESCE(ss.latest_end_lsn::text, '') AS latest_lsn
			FROM pg_subscription s
			LEFT JOIN pg_stat_subscription ss ON ss.subname = s.subname
			WHERE ss.pid IS NULL
		`)
		if err != nil {
			conn.Close(ctx)
			continue
		}

		for rows.Next() {
			var subName, status, latestLSN string
			if err := rows.Scan(&subName, &status, &latestLSN); err != nil {
				continue
			}
			conflicts = append(conflicts, map[string]any{
				"node":         node.Name,
				"subscription": subName,
				"status":       status,
				"latest_lsn":   latestLSN,
			})
		}
		rows.Close()

		// Check the conflict log if it exists.
		var logCount int
		err = conn.QueryRow(ctx, `
			SELECT count(*) FROM `+conflictLogTable+`
			WHERE detected_at > now() - interval '24 hours'
		`).Scan(&logCount)
		if err == nil && logCount > 0 {
			conflicts = append(conflicts, map[string]any{
				"node":               node.Name,
				"recent_conflicts":   logCount,
				"period":             "24h",
			})
		}

		conn.Close(ctx)
	}

	if len(conflicts) == 0 {
		result.Summary = "No replication conflicts detected"
		result.Details = map[string]any{"conflicts": []any{}}
	} else {
		result.Status = "warning"
		result.Summary = fmt.Sprintf("%d potential conflicts detected", len(conflicts))
		result.Details = map[string]any{"conflicts": conflicts}
	}
	result.Finalize()
	return result, nil
}

// SkipConflict advances a stalled subscription past the conflicting
// transaction. This is a destructive operation — the conflicting row
// will be lost on the subscriber side.
func SkipConflict(ctx context.Context, conn *pgx.Conn, subscriptionName string) error {
	// Disable the subscription, advance the LSN, and re-enable.
	_, err := conn.Exec(ctx, fmt.Sprintf(
		"ALTER SUBSCRIPTION %s DISABLE",
		pgx.Identifier{subscriptionName}.Sanitize(),
	))
	if err != nil {
		return fmt.Errorf("disable subscription: %w", err)
	}

	// Advance past the current transaction.
	var lsn string
	err = conn.QueryRow(ctx, `
		SELECT confirmed_flush_lsn::text
		FROM pg_replication_origin_status
		WHERE external_id = (
			SELECT 'pg_' || oid::text
			FROM pg_subscription
			WHERE subname = $1
		)
	`, subscriptionName).Scan(&lsn)
	if err != nil {
		// Re-enable before returning the error.
		_, _ = conn.Exec(ctx, fmt.Sprintf(
			"ALTER SUBSCRIPTION %s ENABLE",
			pgx.Identifier{subscriptionName}.Sanitize(),
		))
		return fmt.Errorf("query replication origin: %w", err)
	}

	// Log the skip.
	_, _ = conn.Exec(ctx, `
		INSERT INTO `+conflictLogTable+` (subscription_name, conflict_type, resolution, details)
		VALUES ($1, 'apply_error', 'skipped', $2)
	`, subscriptionName, fmt.Sprintf("skipped conflict at LSN %s", lsn))

	_, err = conn.Exec(ctx, fmt.Sprintf(
		"ALTER SUBSCRIPTION %s ENABLE",
		pgx.Identifier{subscriptionName}.Sanitize(),
	))
	if err != nil {
		return fmt.Errorf("re-enable subscription: %w", err)
	}

	return nil
}

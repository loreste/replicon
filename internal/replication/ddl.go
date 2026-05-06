package replication

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const ddlTrackingTable = "public.replicon_ddl_log"

// SetupDDLTracking installs event triggers on a node that capture DDL
// statements into a tracking table. The tracking table is excluded from
// publications to avoid replication loops — DDL is replayed by replicon,
// not by PostgreSQL's logical replication.
func SetupDDLTracking(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS `+ddlTrackingTable+` (
			id bigserial PRIMARY KEY,
			event text NOT NULL,
			object_type text,
			schema_name text,
			object_identity text,
			command text NOT NULL,
			executed_at timestamptz NOT NULL DEFAULT now(),
			replayed boolean NOT NULL DEFAULT false,
			source_node text NOT NULL DEFAULT ''
		);

		CREATE OR REPLACE FUNCTION replicon_ddl_trigger() RETURNS event_trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			INSERT INTO `+ddlTrackingTable+` (event, object_type, schema_name, object_identity, command, source_node)
			SELECT
				tg_event,
				object_type,
				schema_name,
				object_identity,
				current_query(),
				''
			FROM pg_event_trigger_ddl_commands()
			WHERE object_type IN ('table', 'index', 'sequence', 'view', 'type', 'function', 'schema')
			LIMIT 1;
		END;
		$$;

		DROP EVENT TRIGGER IF EXISTS replicon_ddl_capture;
		CREATE EVENT TRIGGER replicon_ddl_capture
		ON ddl_command_end
		EXECUTE FUNCTION replicon_ddl_trigger();
	`)
	return err
}

// SyncDDL reads uneplayed DDL from the source node and replays it on the
// target node. Each statement is executed individually and marked as replayed.
func SyncDDL(cfg Config) (CommandResult, error) {
	result := NewCommandResult("ddl-sync", cfg)
	if strings.ToLower(strings.TrimSpace(cfg.Mode)) != "master-master" {
		result.Status = "error"
		result.Summary = "DDL sync is only supported for master-master mode"
		result.Error = result.Summary
		result.Finalize()
		return result, errors.New(result.Error)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nodes := cfg.ResolveLogicalNodes()
	if len(nodes) < 2 {
		result.Status = "error"
		result.Summary = "DDL sync requires at least 2 nodes"
		result.Error = result.Summary
		result.Finalize()
		return result, errors.New(result.Error)
	}

	totalReplayed := 0

	// For each pair of nodes, replay DDL from one to all others.
	for i, source := range nodes {
		sourceDSN, err := source.ResolveDSN()
		if err != nil {
			result.Status = "error"
			result.Error = fmt.Sprintf("resolve %s dsn: %s", source.Name, err)
			result.Finalize()
			return result, errors.New(result.Error)
		}

		sourceConn, err := pgx.Connect(ctx, sourceDSN)
		if err != nil {
			result.Status = "error"
			result.Error = fmt.Sprintf("connect %s: %s", source.Name, redactConnectionError(err))
			result.Finalize()
			return result, errors.New(result.Error)
		}
		defer sourceConn.Close(ctx)

		// Read unreplayed DDL from this source.
		rows, err := sourceConn.Query(ctx, `
			SELECT id, command, object_type, object_identity
			FROM `+ddlTrackingTable+`
			WHERE replayed = false AND source_node = ''
			ORDER BY id
		`)
		if err != nil {
			// Table may not exist yet — not an error.
			continue
		}

		type ddlEntry struct {
			ID             int64
			Command        string
			ObjectType     string
			ObjectIdentity string
		}

		var entries []ddlEntry
		for rows.Next() {
			var e ddlEntry
			if err := rows.Scan(&e.ID, &e.Command, &e.ObjectType, &e.ObjectIdentity); err != nil {
				rows.Close()
				continue
			}
			entries = append(entries, e)
		}
		rows.Close()

		if len(entries) == 0 {
			continue
		}

		// Replay on all other nodes.
		for j, target := range nodes {
			if i == j {
				continue
			}

			targetDSN, err := target.ResolveDSN()
			if err != nil {
				continue
			}

			targetConn, err := pgx.Connect(ctx, targetDSN)
			if err != nil {
				continue
			}

			for _, e := range entries {
				// Disable the event trigger on the target to avoid re-capturing
				// the replayed DDL.
				_, _ = targetConn.Exec(ctx, `
					ALTER EVENT TRIGGER replicon_ddl_capture DISABLE
				`)

				_, execErr := targetConn.Exec(ctx, e.Command)

				_, _ = targetConn.Exec(ctx, `
					ALTER EVENT TRIGGER replicon_ddl_capture ENABLE
				`)

				if execErr != nil {
					// Log but continue — some DDL may already exist on the target.
					continue
				}

				totalReplayed++
			}

			targetConn.Close(ctx)
		}

		// Mark all entries as replayed on the source.
		for _, e := range entries {
			_, _ = sourceConn.Exec(ctx, `
				UPDATE `+ddlTrackingTable+` SET replayed = true WHERE id = $1
			`, e.ID)
		}
	}

	result.Summary = fmt.Sprintf("DDL sync complete: %d statements replayed", totalReplayed)
	result.Details = map[string]any{
		"statements_replayed": totalReplayed,
	}
	result.Finalize()
	return result, nil
}

// SetupDDLTrackingAll installs DDL tracking on all logical replication nodes.
func SetupDDLTrackingAll(cfg Config) (CommandResult, error) {
	result := NewCommandResult("ddl-setup", cfg)
	if strings.ToLower(strings.TrimSpace(cfg.Mode)) != "master-master" {
		result.Status = "error"
		result.Summary = "DDL tracking is only supported for master-master mode"
		result.Error = result.Summary
		result.Finalize()
		return result, errors.New(result.Error)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodes := cfg.ResolveLogicalNodes()
	var installed []string

	for _, node := range nodes {
		dsn, err := node.ResolveDSN()
		if err != nil {
			result.Status = "error"
			result.Error = fmt.Sprintf("resolve %s dsn: %s", node.Name, err)
			result.Finalize()
			return result, errors.New(result.Error)
		}

		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			result.Status = "error"
			result.Error = fmt.Sprintf("connect %s: %s", node.Name, redactConnectionError(err))
			result.Finalize()
			return result, errors.New(result.Error)
		}

		if err := SetupDDLTracking(ctx, conn); err != nil {
			conn.Close(ctx)
			result.Status = "error"
			result.Error = fmt.Sprintf("setup DDL tracking on %s: %s", node.Name, err)
			result.Finalize()
			return result, errors.New(result.Error)
		}

		conn.Close(ctx)
		installed = append(installed, node.Name)
	}

	result.Summary = fmt.Sprintf("DDL tracking installed on: %s", strings.Join(installed, ", "))
	result.Details = map[string]any{
		"nodes": installed,
	}
	result.Finalize()
	return result, nil
}

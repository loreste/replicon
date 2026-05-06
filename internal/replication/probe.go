package replication

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const probeTableName = "public.replicon_replication_probe"

func ProbeReplication(cfg Config) (CommandResult, error) {
	result := NewCommandResult("probe", cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var (
		out CommandResult
		err error
	)

	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "master-slave":
		out, err = probeMasterSlave(ctx, cfg, result)
	case "master-master":
		out, err = probeMasterMaster(ctx, cfg, result)
	default:
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("unsupported mode %q", cfg.Mode)
		result.Finalize()
		return result, errors.New(result.Error)
	}

	out.Finalize()
	return out, err
}

func probeMasterSlave(ctx context.Context, cfg Config, result CommandResult) (CommandResult, error) {
	primaryDSN, err := cfg.Primary.ResolveDSN()
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("resolve primary dsn: %s", err)
		return result, errors.New(result.Error)
	}

	primaryConn, err := pgx.Connect(ctx, primaryDSN)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("connect primary: %s", redactConnectionError(err))
		return result, errors.New(result.Error)
	}
	defer primaryConn.Close(ctx)

	if err := ensureProbeTable(ctx, primaryConn); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("ensure probe table on primary: %s", err)
		return result, errors.New(result.Error)
	}

	standbys := cfg.ResolveStandbys()
	var probeResults []any
	var probeTargets []string

	for _, standby := range standbys {
		label := standby.Name

		standbyDSN, err := standby.ResolveDSN()
		if err != nil {
			result.Status = "error"
			result.Summary = "Replication probe failed"
			result.Error = fmt.Sprintf("resolve %s dsn: %s", label, err)
			return result, errors.New(result.Error)
		}

		standbyConn, err := pgx.Connect(ctx, standbyDSN)
		if err != nil {
			result.Status = "error"
			result.Summary = "Replication probe failed"
			result.Error = fmt.Sprintf("connect %s: %s", label, redactConnectionError(err))
			return result, errors.New(result.Error)
		}
		defer standbyConn.Close(ctx)

		probeID := newProbeID(cfg.Primary.Name, standby.Name)

		if err := insertProbeRow(ctx, primaryConn, probeID, cfg.Primary.Name); err != nil {
			result.Status = "error"
			result.Summary = "Replication probe failed"
			result.Error = fmt.Sprintf("insert probe row for %s: %s", label, err)
			return result, errors.New(result.Error)
		}
		defer cleanupProbeRow(context.Background(), primaryDSN, probeID)

		if err := waitForProbeRow(ctx, standbyConn, probeID, true); err != nil {
			result.Status = "error"
			result.Summary = "Replication probe failed"
			result.Error = fmt.Sprintf("probe row did not appear on %s: %s", label, err)
			return result, errors.New(result.Error)
		}

		if err := deleteProbeRow(ctx, primaryConn, probeID); err != nil {
			result.Status = "error"
			result.Summary = "Replication probe failed"
			result.Error = fmt.Sprintf("delete probe row for %s: %s", label, err)
			return result, errors.New(result.Error)
		}

		if err := waitForProbeRow(ctx, standbyConn, probeID, false); err != nil {
			result.Status = "error"
			result.Summary = "Replication probe failed"
			result.Error = fmt.Sprintf("probe row deletion did not reach %s: %s", label, err)
			return result, errors.New(result.Error)
		}

		probeTargets = append(probeTargets, label)
		probeResults = append(probeResults, map[string]any{
			"probe_id": probeID,
			"source":   cfg.Primary.Name,
			"target":   label,
		})
	}

	targetList := strings.Join(probeTargets, ", ")
	result.Summary = fmt.Sprintf("Active replication probe: PASS\nMode: master-slave\nProbe table: %s\nWrote on: %s\nObserved on: %s\nDeletion replay confirmed on all standbys\n",
		probeTableName, cfg.Primary.Name, targetList)
	result.Details = map[string]any{
		"probe_table": probeTableName,
		"source":      cfg.Primary.Name,
	}
	if len(probeResults) == 1 {
		result.Details["probe_id"] = probeResults[0].(map[string]any)["probe_id"]
		result.Details["target"] = probeTargets[0]
	} else {
		result.Details["targets"] = probeResults
	}
	return result, nil
}

func probeMasterMaster(ctx context.Context, cfg Config, result CommandResult) (CommandResult, error) {
	nodeADSN, err := cfg.NodeA.ResolveDSN()
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("resolve node-a dsn: %s", err)
		return result, errors.New(result.Error)
	}
	nodeBDSN, err := cfg.NodeB.ResolveDSN()
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("resolve node-b dsn: %s", err)
		return result, errors.New(result.Error)
	}

	nodeAConn, err := pgx.Connect(ctx, nodeADSN)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("connect node-a: %s", redactConnectionError(err))
		return result, errors.New(result.Error)
	}
	defer nodeAConn.Close(ctx)

	nodeBConn, err := pgx.Connect(ctx, nodeBDSN)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("connect node-b: %s", redactConnectionError(err))
		return result, errors.New(result.Error)
	}
	defer nodeBConn.Close(ctx)

	if err := ensureProbeTable(ctx, nodeAConn); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("ensure probe table on node-a: %s", err)
		return result, errors.New(result.Error)
	}
	if err := ensureProbeTable(ctx, nodeBConn); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("ensure probe table on node-b: %s", err)
		return result, errors.New(result.Error)
	}
	if err := refreshSubscription(ctx, nodeAConn, cfg.Logical.SubscriptionA); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("refresh node-a subscription: %s", err)
		return result, errors.New(result.Error)
	}
	if err := refreshSubscription(ctx, nodeBConn, cfg.Logical.SubscriptionB); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("refresh node-b subscription: %s", err)
		return result, errors.New(result.Error)
	}

	forwardProbeID := newProbeID(cfg.NodeA.Name, cfg.NodeB.Name)
	if err := insertProbeRow(ctx, nodeAConn, forwardProbeID, cfg.NodeA.Name); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("insert probe row on node-a: %s", err)
		return result, errors.New(result.Error)
	}
	defer cleanupProbeRow(context.Background(), nodeADSN, forwardProbeID)

	if err := waitForProbeRow(ctx, nodeBConn, forwardProbeID, true); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("probe row from node-a did not appear on node-b: %s", err)
		return result, errors.New(result.Error)
	}

	if err := deleteProbeRow(ctx, nodeAConn, forwardProbeID); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("delete probe row on node-a: %s", err)
		return result, errors.New(result.Error)
	}

	if err := waitForProbeRow(ctx, nodeBConn, forwardProbeID, false); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("probe row deletion from node-a did not reach node-b: %s", err)
		return result, errors.New(result.Error)
	}

	reverseProbeID := newProbeID(cfg.NodeB.Name, cfg.NodeA.Name)
	if err := insertProbeRow(ctx, nodeBConn, reverseProbeID, cfg.NodeB.Name); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("insert probe row on node-b: %s", err)
		return result, errors.New(result.Error)
	}
	defer cleanupProbeRow(context.Background(), nodeBDSN, reverseProbeID)

	if err := waitForProbeRow(ctx, nodeAConn, reverseProbeID, true); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("probe row from node-b did not appear on node-a: %s", err)
		return result, errors.New(result.Error)
	}

	if err := deleteProbeRow(ctx, nodeBConn, reverseProbeID); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("delete probe row on node-b: %s", err)
		return result, errors.New(result.Error)
	}

	if err := waitForProbeRow(ctx, nodeAConn, reverseProbeID, false); err != nil {
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = fmt.Sprintf("probe row deletion from node-b did not reach node-a: %s", err)
		return result, errors.New(result.Error)
	}

	result.Summary = fmt.Sprintf(`Active replication probe: PASS
Mode: master-master
Probe table: %s
Forward path: %s -> %s
Reverse path: %s -> %s
Insert and delete propagation confirmed in both directions
`, probeTableName, cfg.NodeA.Name, cfg.NodeB.Name, cfg.NodeB.Name, cfg.NodeA.Name)
	result.Details = map[string]any{
		"probe_table": probeTableName,
		"forward": map[string]any{
			"probe_id": forwardProbeID,
			"source":   cfg.NodeA.Name,
			"target":   cfg.NodeB.Name,
		},
		"reverse": map[string]any{
			"probe_id": reverseProbeID,
			"source":   cfg.NodeB.Name,
			"target":   cfg.NodeA.Name,
		},
	}
	return result, nil
}

func ensureProbeTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.replicon_replication_probe (
			probe_id text PRIMARY KEY,
			source_node text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)
	`)
	return err
}

func refreshSubscription(ctx context.Context, conn *pgx.Conn, name string) error {
	_, err := conn.Exec(ctx, fmt.Sprintf(
		"ALTER SUBSCRIPTION %s REFRESH PUBLICATION WITH (copy_data = false)",
		pgx.Identifier{name}.Sanitize(),
	))
	return err
}

func insertProbeRow(ctx context.Context, conn *pgx.Conn, probeID, sourceNode string) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO public.replicon_replication_probe (probe_id, source_node)
		VALUES ($1, $2)
	`, probeID, sourceNode)
	return err
}

func deleteProbeRow(ctx context.Context, conn *pgx.Conn, probeID string) error {
	_, err := conn.Exec(ctx, `
		DELETE FROM public.replicon_replication_probe
		WHERE probe_id = $1
	`, probeID)
	return err
}

func waitForProbeRow(ctx context.Context, conn *pgx.Conn, probeID string, wantPresent bool) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		match, err := hasProbeRow(ctx, conn, probeID)
		if err != nil {
			return err
		}
		if match == wantPresent {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func hasProbeRow(ctx context.Context, conn *pgx.Conn, probeID string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM public.replicon_replication_probe
			WHERE probe_id = $1
		)
	`, probeID).Scan(&exists)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return false, nil
		}
	}
	return exists, err
}

func cleanupProbeRow(_ context.Context, dsn, probeID string) {
	if strings.TrimSpace(dsn) == "" || strings.TrimSpace(probeID) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return
	}
	defer conn.Close(ctx)

	_, _ = conn.Exec(ctx, `
		DELETE FROM public.replicon_replication_probe
		WHERE probe_id = $1
	`, probeID)
}

func newProbeID(source, target string) string {
	return fmt.Sprintf("replicon-%s-to-%s-%d", sanitizeProbeSegment(source), sanitizeProbeSegment(target), time.Now().UnixNano())
}

func sanitizeProbeSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	if value == "" {
		return "node"
	}
	return value
}

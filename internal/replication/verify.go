package replication

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	uriPattern      = regexp.MustCompile(`postgres://[^[:space:]]+`)
	passwordPattern = regexp.MustCompile(`password=[^[:space:]]+`)
)

func VerifyReplication(cfg Config) (CommandResult, error) {
	result := NewCommandResult("verify", cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		out CommandResult
		err error
	)

	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "master-slave":
		out, err = verifyMasterSlave(ctx, cfg, result)
	case "master-master":
		out, err = verifyMasterMaster(ctx, cfg, result)
	default:
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("unsupported mode %q", cfg.Mode)
		result.Finalize()
		return result, errors.New(result.Error)
	}

	out.Finalize()
	return out, err
}

func verifyMasterSlave(ctx context.Context, cfg Config, result CommandResult) (CommandResult, error) {
	primaryDSN, err := cfg.Primary.ResolveDSN()
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("resolve primary dsn: %s", err)
		return result, errors.New(result.Error)
	}

	primaryConn, err := pgx.Connect(ctx, primaryDSN)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("connect primary: %s", redactConnectionError(err))
		return result, errors.New(result.Error)
	}
	defer primaryConn.Close(ctx)

	standbys := cfg.ResolveStandbys()
	var summaryLines []string
	summaryLines = append(summaryLines, "Replication verification: PASS")
	summaryLines = append(summaryLines, "Mode: master-slave")
	summaryLines = append(summaryLines, fmt.Sprintf("Primary: %s", cfg.Primary.Name))

	primaryDetails := map[string]any{"name": cfg.Primary.Name}
	standbyDetails := make([]any, 0, len(standbys))

	for i, standby := range standbys {
		label := standby.Name
		if label == "" {
			label = fmt.Sprintf("standby-%d", i)
		}

		standbyDSN, err := standby.ResolveDSN()
		if err != nil {
			result.Status = "error"
			result.Summary = "Replication verification failed"
			result.Error = fmt.Sprintf("resolve %s dsn: %s", label, err)
			return result, errors.New(result.Error)
		}

		standbyConn, err := pgx.Connect(ctx, standbyDSN)
		if err != nil {
			result.Status = "error"
			result.Summary = "Replication verification failed"
			result.Error = fmt.Sprintf("connect %s: %s", label, redactConnectionError(err))
			return result, errors.New(result.Error)
		}
		defer standbyConn.Close(ctx)

		// Query primary's view of this standby.
		var applicationName, clientAddr, state, syncState string
		err = primaryConn.QueryRow(ctx, `
			SELECT application_name, client_addr::text, state, sync_state
			FROM pg_stat_replication
			WHERE client_addr::text = $1
			ORDER BY application_name
			LIMIT 1
		`, standby.Host).Scan(&applicationName, &clientAddr, &state, &syncState)
		if err != nil {
			// Fall back to application_name match for single-standby compat.
			err = primaryConn.QueryRow(ctx, `
				SELECT application_name, client_addr::text, state, sync_state
				FROM pg_stat_replication
				WHERE application_name = $1
				ORDER BY application_name
				LIMIT 1
			`, cfg.Network.ApplicationName).Scan(&applicationName, &clientAddr, &state, &syncState)
		}
		if err != nil {
			result.Status = "error"
			result.Summary = "Replication verification failed"
			result.Error = fmt.Sprintf("query primary replication state for %s: %s", label, err)
			return result, errors.New(result.Error)
		}

		// Query standby's own view.
		var inRecovery bool
		var receiveLSN, replayLSN string
		var replayDelaySeconds float64
		err = standbyConn.QueryRow(ctx, `
			SELECT
				pg_is_in_recovery(),
				COALESCE(pg_last_wal_receive_lsn()::text, ''),
				COALESCE(pg_last_wal_replay_lsn()::text, ''),
				COALESCE(EXTRACT(EPOCH FROM now() - pg_last_xact_replay_timestamp()), 0)
		`).Scan(&inRecovery, &receiveLSN, &replayLSN, &replayDelaySeconds)
		if err != nil {
			result.Status = "error"
			result.Summary = "Replication verification failed"
			result.Error = fmt.Sprintf("query %s replay state: %s", label, err)
			return result, errors.New(result.Error)
		}

		if !inRecovery {
			result.Status = "error"
			result.Summary = "Replication verification failed"
			result.Error = fmt.Sprintf("%s is not in recovery mode", label)
			return result, errors.New(result.Error)
		}
		if state != "streaming" {
			result.Status = "error"
			result.Summary = "Replication verification failed"
			result.Error = fmt.Sprintf("primary sees %s state %q instead of streaming", label, state)
			return result, errors.New(result.Error)
		}

		summaryLines = append(summaryLines, fmt.Sprintf(
			"%s: application_name=%s client_addr=%s state=%s sync_state=%s in_recovery=%t receive_lsn=%s replay_lsn=%s replay_delay_seconds=%.0f",
			label, applicationName, clientAddr, state, syncState, inRecovery, receiveLSN, replayLSN, replayDelaySeconds,
		))

		if i == 0 {
			primaryDetails["application_name"] = applicationName
			primaryDetails["client_addr"] = clientAddr
			primaryDetails["state"] = state
			primaryDetails["sync_state"] = syncState
		}

		standbyDetails = append(standbyDetails, map[string]any{
			"name":                 label,
			"in_recovery":          inRecovery,
			"receive_lsn":          receiveLSN,
			"replay_lsn":           replayLSN,
			"replay_delay_seconds": replayDelaySeconds,
		})
	}

	result.Summary = strings.Join(summaryLines, "\n") + "\n"
	result.Details = map[string]any{
		"primary": primaryDetails,
	}
	// For backward compatibility: single standby uses "standby" key, cluster uses "standbys".
	if len(standbyDetails) == 1 {
		result.Details["standby"] = standbyDetails[0]
	} else {
		result.Details["standbys"] = standbyDetails
	}
	return result, nil
}

func verifyMasterMaster(ctx context.Context, cfg Config, result CommandResult) (CommandResult, error) {
	nodeADSN, err := cfg.NodeA.ResolveDSN()
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("resolve node-a dsn: %s", err)
		return result, errors.New(result.Error)
	}
	nodeBDSN, err := cfg.NodeB.ResolveDSN()
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("resolve node-b dsn: %s", err)
		return result, errors.New(result.Error)
	}

	nodeAConn, err := pgx.Connect(ctx, nodeADSN)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("connect node-a: %s", redactConnectionError(err))
		return result, errors.New(result.Error)
	}
	defer nodeAConn.Close(ctx)

	nodeBConn, err := pgx.Connect(ctx, nodeBDSN)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("connect node-b: %s", redactConnectionError(err))
		return result, errors.New(result.Error)
	}
	defer nodeBConn.Close(ctx)

	aStatus, err := fetchSubscriptionStatus(ctx, nodeAConn, cfg.Logical.SubscriptionA)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("query node-a subscription: %s", err)
		return result, errors.New(result.Error)
	}

	bStatus, err := fetchSubscriptionStatus(ctx, nodeBConn, cfg.Logical.SubscriptionB)
	if err != nil {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("query node-b subscription: %s", err)
		return result, errors.New(result.Error)
	}

	if aStatus.Status != "streaming" {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("node-a subscription %q is %q instead of streaming", aStatus.Name, aStatus.Status)
		return result, errors.New(result.Error)
	}
	if bStatus.Status != "streaming" {
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = fmt.Sprintf("node-b subscription %q is %q instead of streaming", bStatus.Name, bStatus.Status)
		return result, errors.New(result.Error)
	}

	result.Summary = fmt.Sprintf(`Replication verification: PASS
Mode: master-master
Node A: %s subscription=%s status=%s received_lsn=%s latest_end_lsn=%s
Node B: %s subscription=%s status=%s received_lsn=%s latest_end_lsn=%s
`,
		cfg.NodeA.Name,
		aStatus.Name,
		aStatus.Status,
		aStatus.ReceivedLSN,
		aStatus.LatestEndLSN,
		cfg.NodeB.Name,
		bStatus.Name,
		bStatus.Status,
		bStatus.ReceivedLSN,
		bStatus.LatestEndLSN,
	)
	result.Details = map[string]any{
		"node_a": map[string]any{
			"name":           cfg.NodeA.Name,
			"subscription":   aStatus.Name,
			"status":         aStatus.Status,
			"received_lsn":   aStatus.ReceivedLSN,
			"latest_end_lsn": aStatus.LatestEndLSN,
		},
		"node_b": map[string]any{
			"name":           cfg.NodeB.Name,
			"subscription":   bStatus.Name,
			"status":         bStatus.Status,
			"received_lsn":   bStatus.ReceivedLSN,
			"latest_end_lsn": bStatus.LatestEndLSN,
		},
	}
	return result, nil
}

type subscriptionStatus struct {
	Name         string
	Status       string
	ReceivedLSN  string
	LatestEndLSN string
}

func fetchSubscriptionStatus(ctx context.Context, conn *pgx.Conn, name string) (subscriptionStatus, error) {
	var status subscriptionStatus
	err := conn.QueryRow(ctx, `
		SELECT subname,
		       CASE WHEN pid IS NULL THEN 'down' ELSE 'streaming' END,
		       COALESCE(received_lsn::text, ''),
		       COALESCE(latest_end_lsn::text, '')
		FROM pg_stat_subscription
		WHERE subname = $1
	`, name).Scan(&status.Name, &status.Status, &status.ReceivedLSN, &status.LatestEndLSN)
	if err != nil {
		return subscriptionStatus{}, err
	}

	return status, nil
}

func redactConnectionError(err error) string {
	text := err.Error()
	text = uriPattern.ReplaceAllString(text, "[redacted-dsn]")
	text = passwordPattern.ReplaceAllString(text, "password=[redacted]")
	return text
}

package replication

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// WatchdogEvent describes what happened during a watchdog check cycle.
type WatchdogEvent struct {
	Time          time.Time `json:"time"`
	Type          string    `json:"type"` // "healthy", "failure", "fence", "promote", "error"
	Message       string    `json:"message"`
	Failures      int       `json:"failures"`
	MaxFailures   int       `json:"max_failures"`
	FenceSuccess  bool      `json:"fence_success,omitempty"`
	PromoteResult string    `json:"promote_result,omitempty"`
}

// WatchdogCallbacks allows callers to react to watchdog events without
// coupling the watchdog to a specific output mechanism.
type WatchdogCallbacks struct {
	OnEvent func(WatchdogEvent)
}

// WatchdogDefaults fills in zero-valued failover config fields with safe
// defaults.
func WatchdogDefaults(f Failover) Failover {
	if f.CheckIntervalSec <= 0 {
		f.CheckIntervalSec = 5
	}
	if f.HealthTimeoutSec <= 0 {
		f.HealthTimeoutSec = 3
	}
	if f.MaxFailures <= 0 {
		f.MaxFailures = 3
	}
	if f.FenceTimeoutSec <= 0 {
		f.FenceTimeoutSec = 10
	}
	return f
}

// RunWatchdog monitors the primary and triggers failover when it becomes
// unreachable. It blocks until ctx is cancelled or a failover completes.
//
// Safety model:
//  1. Check primary health via SQL connection every CheckIntervalSec.
//  2. After MaxFailures consecutive failures, attempt to fence the primary
//     via SSH (stop PostgreSQL). This prevents split-brain.
//  3. Only promote the standby if fencing succeeds. If we cannot fence
//     (e.g. network partition), we do not promote, because we cannot be
//     sure the primary is actually down.
//  4. After promotion, run the optional PostPromoteCommand if set.
//
// Returns nil when failover completes successfully, the context error when
// cancelled, or an error if failover was attempted but failed.
func RunWatchdog(ctx context.Context, cfg Config, service *Service, cb WatchdogCallbacks) error {
	if strings.ToLower(strings.TrimSpace(cfg.Mode)) != "master-slave" {
		return fmt.Errorf("automatic failover is only supported for master-slave mode")
	}

	f := WatchdogDefaults(cfg.Failover)
	interval := time.Duration(f.CheckIntervalSec) * time.Second
	healthTimeout := time.Duration(f.HealthTimeoutSec) * time.Second
	fenceTimeout := time.Duration(f.FenceTimeoutSec) * time.Second

	primaryDSN, err := cfg.Primary.ResolveDSN()
	if err != nil {
		return fmt.Errorf("resolve primary dsn: %w", err)
	}

	emit := func(e WatchdogEvent) {
		if cb.OnEvent != nil {
			cb.OnEvent(e)
		}
	}

	consecutiveFailures := 0
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	emit(WatchdogEvent{
		Time:    time.Now().UTC(),
		Type:    "healthy",
		Message: fmt.Sprintf("watchdog started: checking primary every %ds, failover after %d consecutive failures", f.CheckIntervalSec, f.MaxFailures),
	})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		healthy := checkPrimaryHealth(ctx, primaryDSN, healthTimeout)

		if healthy {
			if consecutiveFailures > 0 {
				emit(WatchdogEvent{
					Time:        time.Now().UTC(),
					Type:        "healthy",
					Message:     fmt.Sprintf("primary recovered after %d failures", consecutiveFailures),
					Failures:    0,
					MaxFailures: f.MaxFailures,
				})
			}
			consecutiveFailures = 0
			continue
		}

		consecutiveFailures++
		emit(WatchdogEvent{
			Time:        time.Now().UTC(),
			Type:        "failure",
			Message:     fmt.Sprintf("primary health check failed (%d/%d)", consecutiveFailures, f.MaxFailures),
			Failures:    consecutiveFailures,
			MaxFailures: f.MaxFailures,
		})

		if consecutiveFailures < f.MaxFailures {
			continue
		}

		// Threshold reached — attempt fence-then-promote.
		return executeFenceAndPromote(ctx, cfg, f, fenceTimeout, service, emit)
	}
}

// checkPrimaryHealth attempts a SQL connection and a simple query to confirm
// the primary is up and writable.
func checkPrimaryHealth(ctx context.Context, dsn string, timeout time.Duration) bool {
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := pgx.Connect(hctx, dsn)
	if err != nil {
		return false
	}
	defer conn.Close(hctx)

	var inRecovery bool
	err = conn.QueryRow(hctx, "SELECT pg_is_in_recovery()").Scan(&inRecovery)
	if err != nil {
		return false
	}
	// If the primary is in recovery, it has already been demoted — not healthy
	// as a primary.
	return !inRecovery
}

// executeFenceAndPromote fences the old primary, then promotes the standby.
func executeFenceAndPromote(
	ctx context.Context,
	cfg Config,
	f Failover,
	fenceTimeout time.Duration,
	service *Service,
	emit func(WatchdogEvent),
) error {
	// Step 1: Fence the primary (stop PostgreSQL via SSH).
	fenceCmd := f.FenceCommand
	if fenceCmd == "" {
		fenceCmd = "sudo systemctl stop postgresql"
	}

	emit(WatchdogEvent{
		Time:    time.Now().UTC(),
		Type:    "fence",
		Message: fmt.Sprintf("fencing primary %s via SSH", cfg.Primary.Name),
	})

	fenceErr := executeRemoteCommand(cfg.Primary, fenceCmd, fenceTimeout)
	if fenceErr != nil {
		// Cannot fence — do NOT promote. We might be in a network partition
		// where the primary is still running.
		event := WatchdogEvent{
			Time:         time.Now().UTC(),
			Type:         "error",
			Message:      fmt.Sprintf("fencing failed, promotion aborted: %s", fenceErr),
			FenceSuccess: false,
		}
		emit(event)

		if service != nil {
			result := NewCommandResult("watch-fence", cfg)
			result.Status = "error"
			result.Summary = "Fencing failed — automatic promotion aborted to prevent split-brain"
			result.Error = fenceErr.Error()
			result.Finalize()
			service.record(result)
		}

		return fmt.Errorf("fencing primary failed, promotion aborted: %w", fenceErr)
	}

	emit(WatchdogEvent{
		Time:         time.Now().UTC(),
		Type:         "fence",
		Message:      fmt.Sprintf("primary %s fenced successfully", cfg.Primary.Name),
		FenceSuccess: true,
	})

	// Step 2: Promote the standby.
	emit(WatchdogEvent{
		Time:    time.Now().UTC(),
		Type:    "promote",
		Message: fmt.Sprintf("promoting standby %s", cfg.Standby.Name),
	})

	result, promoteErr := PromoteStandby(cfg, OperationOptions{Execute: true, Timeout: 5 * time.Minute})
	if service != nil {
		result.Action = "watch-promote"
		service.record(result)
	}

	if promoteErr != nil {
		emit(WatchdogEvent{
			Time:          time.Now().UTC(),
			Type:          "error",
			Message:       fmt.Sprintf("promotion failed: %s", promoteErr),
			PromoteResult: "failed",
		})
		return fmt.Errorf("promotion failed: %w", promoteErr)
	}

	emit(WatchdogEvent{
		Time:          time.Now().UTC(),
		Type:          "promote",
		Message:       fmt.Sprintf("promotion complete: %s is now primary", cfg.Standby.Name),
		PromoteResult: "ok",
	})

	// Step 3: Optional post-promote command (e.g. update DNS, notify load balancer).
	if strings.TrimSpace(f.PostPromoteCommand) != "" {
		log.Printf("replicon: running post-promote command on %s", cfg.Standby.Name)
		if err := executeRemoteCommand(cfg.Standby, f.PostPromoteCommand, fenceTimeout); err != nil {
			log.Printf("replicon: post-promote command failed (non-fatal): %v", err)
		}
	}

	return nil
}

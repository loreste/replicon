package replication

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	electionTable     = "public.replicon_leader"
	defaultLeaseTTL   = 30 // seconds
	defaultRenewEvery = 10 // seconds
)

// LeaderElection manages leader election via a PostgreSQL coordination
// database. Multiple replicon agents compete for a lease row. The holder
// of the lease is the leader and the only agent allowed to trigger
// failover.
//
// How it works:
//  1. A single row in replicon_leader tracks the current leader (node_id)
//     and the lease expiry time.
//  2. An agent becomes leader by inserting or updating the row when no
//     valid lease exists (expired or missing).
//  3. The leader renews the lease periodically (every RenewSec).
//  4. If the leader stops renewing (crash, network issue), the lease
//     expires and another agent can claim it.
//  5. All operations use SELECT ... FOR UPDATE SKIP LOCKED to avoid
//     blocking — if another agent holds the row lock, we skip and
//     try again next cycle.
type LeaderElection struct {
	cfg      ElectionConfig
	cluster  string
	mu       sync.Mutex
	isLeader bool
	cancel   context.CancelFunc
}

// NewLeaderElection creates a new election instance. Call Run() to start
// the election loop.
func NewLeaderElection(cfg ElectionConfig, clusterName string) *LeaderElection {
	return &LeaderElection{
		cfg:     cfg,
		cluster: clusterName,
	}
}

// IsLeader returns true if this agent currently holds the leader lease.
func (le *LeaderElection) IsLeader() bool {
	le.mu.Lock()
	defer le.mu.Unlock()
	return le.isLeader
}

func (le *LeaderElection) setLeader(v bool) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.isLeader = v
}

// Run starts the election loop. It blocks until ctx is cancelled.
// The caller should check IsLeader() to determine if this agent
// is the current leader before taking failover actions.
func (le *LeaderElection) Run(ctx context.Context, emit func(WatchdogEvent)) error {
	dsn, err := le.cfg.ResolveDSN()
	if err != nil {
		return fmt.Errorf("resolve election dsn: %w", err)
	}

	leaseTTL := le.cfg.LeaseTTLSec
	if leaseTTL <= 0 {
		leaseTTL = defaultLeaseTTL
	}
	renewEvery := le.cfg.RenewSec
	if renewEvery <= 0 {
		renewEvery = defaultRenewEvery
	}

	// Ensure the leader table exists.
	if err := le.ensureTable(ctx, dsn); err != nil {
		return fmt.Errorf("create election table: %w", err)
	}

	ticker := time.NewTicker(time.Duration(renewEvery) * time.Second)
	defer ticker.Stop()

	// Try immediately on startup.
	le.tryAcquireOrRenew(ctx, dsn, leaseTTL, emit)

	for {
		select {
		case <-ctx.Done():
			le.setLeader(false)
			le.releaseLease(dsn)
			return ctx.Err()
		case <-ticker.C:
			le.tryAcquireOrRenew(ctx, dsn, leaseTTL, emit)
		}
	}
}

func (le *LeaderElection) ensureTable(ctx context.Context, dsn string) error {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(tctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(tctx)

	_, err = conn.Exec(tctx, `
		CREATE TABLE IF NOT EXISTS `+electionTable+` (
			cluster_name text PRIMARY KEY,
			leader_id text NOT NULL,
			lease_until timestamptz NOT NULL,
			acquired_at timestamptz NOT NULL DEFAULT now(),
			renewals bigint NOT NULL DEFAULT 0
		)
	`)
	return err
}

func (le *LeaderElection) tryAcquireOrRenew(ctx context.Context, dsn string, leaseTTL int, emit func(WatchdogEvent)) {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := pgx.Connect(tctx, dsn)
	if err != nil {
		le.setLeader(false)
		return
	}
	defer conn.Close(tctx)

	tx, err := conn.Begin(tctx)
	if err != nil {
		le.setLeader(false)
		return
	}
	defer tx.Rollback(tctx)

	ttlInterval := fmt.Sprintf("%d seconds", leaseTTL)

	// Try to lock the existing row.
	var currentLeader string
	var leaseUntil time.Time
	err = tx.QueryRow(tctx, `
		SELECT leader_id, lease_until
		FROM `+electionTable+`
		WHERE cluster_name = $1
		FOR UPDATE SKIP LOCKED
	`, le.cluster).Scan(&currentLeader, &leaseUntil)

	if err != nil {
		// Row doesn't exist or is locked by another agent.
		// Try to insert (first agent to start).
		_, insertErr := tx.Exec(tctx, `
			INSERT INTO `+electionTable+` (cluster_name, leader_id, lease_until)
			VALUES ($1, $2, now() + $3::interval)
			ON CONFLICT (cluster_name) DO NOTHING
		`, le.cluster, le.cfg.NodeID, ttlInterval)
		if insertErr != nil {
			le.setLeader(false)
			return
		}

		if err := tx.Commit(tctx); err != nil {
			le.setLeader(false)
			return
		}

		wasLeader := le.IsLeader()
		le.setLeader(true)
		if !wasLeader {
			emit(WatchdogEvent{
				Time:    time.Now().UTC(),
				Type:    "election",
				Message: fmt.Sprintf("acquired leader lease for cluster %s (node %s)", le.cluster, le.cfg.NodeID),
			})
		}
		return
	}

	now := time.Now().UTC()

	if currentLeader == le.cfg.NodeID {
		// We hold the lease — renew it.
		_, err = tx.Exec(tctx, `
			UPDATE `+electionTable+`
			SET lease_until = now() + $1::interval, renewals = renewals + 1
			WHERE cluster_name = $2 AND leader_id = $3
		`, ttlInterval, le.cluster, le.cfg.NodeID)
		if err != nil {
			le.setLeader(false)
			return
		}
		if err := tx.Commit(tctx); err != nil {
			le.setLeader(false)
			return
		}
		le.setLeader(true)
		return
	}

	// Another agent holds the lease.
	if now.Before(leaseUntil) {
		// Lease is still valid — we are not the leader.
		wasLeader := le.IsLeader()
		le.setLeader(false)
		if wasLeader {
			emit(WatchdogEvent{
				Time:    time.Now().UTC(),
				Type:    "election",
				Message: fmt.Sprintf("lost leader lease to %s (lease valid until %s)", currentLeader, leaseUntil.Format(time.RFC3339)),
			})
		}
		return
	}

	// Lease has expired — take over.
	_, err = tx.Exec(tctx, `
		UPDATE `+electionTable+`
		SET leader_id = $1, lease_until = now() + $2::interval, acquired_at = now(), renewals = 0
		WHERE cluster_name = $3
	`, le.cfg.NodeID, ttlInterval, le.cluster)
	if err != nil {
		le.setLeader(false)
		return
	}
	if err := tx.Commit(tctx); err != nil {
		le.setLeader(false)
		return
	}

	emit(WatchdogEvent{
		Time:    time.Now().UTC(),
		Type:    "election",
		Message: fmt.Sprintf("took over leader lease from %s (expired at %s)", currentLeader, leaseUntil.Format(time.RFC3339)),
	})
	le.setLeader(true)
}

// releaseLease voluntarily gives up the lease on shutdown.
func (le *LeaderElection) releaseLease(dsn string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return
	}
	defer conn.Close(ctx)

	_, _ = conn.Exec(ctx, `
		UPDATE `+electionTable+`
		SET lease_until = now() - interval '1 second'
		WHERE cluster_name = $1 AND leader_id = $2
	`, le.cluster, le.cfg.NodeID)
}

// CurrentLeader queries the coordination database and returns the current
// leader's node ID and lease expiry. Returns empty strings if no leader.
func CurrentLeader(ctx context.Context, dsn, clusterName string) (nodeID string, leaseUntil time.Time, err error) {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := pgx.Connect(tctx, dsn)
	if err != nil {
		return "", time.Time{}, err
	}
	defer conn.Close(tctx)

	err = conn.QueryRow(tctx, `
		SELECT leader_id, lease_until
		FROM `+electionTable+`
		WHERE cluster_name = $1
	`, clusterName).Scan(&nodeID, &leaseUntil)
	if err != nil {
		return "", time.Time{}, err
	}

	return nodeID, leaseUntil, nil
}

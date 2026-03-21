// Package observer - beads.go
//
// Implements ReadBeads, which queries the Dolt bd_issues table for open-issue
// counts and recent closed-issue latencies. Per ADR-0011 D3 and D4.
package observer

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// BeadsKey identifies a unique (type, priority) bucket for open-issue counts.
type BeadsKey struct {
	Type     string
	Priority int
}

// LatencySample holds the filing-to-closing duration for a single closed issue.
type LatencySample struct {
	Type    string
	Seconds float64
}

// BeadsSnapshot holds a point-in-time read of Beads workflow state from Dolt.
// OpenByTypePriority may be empty when no open issues exist. RecentLatencies
// may be empty when no issues were closed within the requested window.
type BeadsSnapshot struct {
	// OpenByTypePriority maps (type, priority) → count of open/in_progress issues.
	OpenByTypePriority map[BeadsKey]int64

	// RecentLatencies contains one entry per issue closed within the window.
	RecentLatencies []LatencySample

	ReadAt time.Time
}

// ReadBeads queries bd_issues for open-issue counts by (type, priority) and
// latency samples for issues closed within window. Both queries are run
// independently (D4): a failure in one does not prevent the other from
// completing. If both fail, the returned error is non-nil and the snapshot
// fields will be nil/empty. If only one fails, that error is included in the
// returned error while the other field is populated.
func ReadBeads(ctx context.Context, db *sql.DB, window time.Duration) (BeadsSnapshot, error) {
	snap := BeadsSnapshot{ReadAt: time.Now()}
	var errs []error

	// --- Group 1: open + in_progress counts by (type, priority) ---
	counts, err := readBeadsOpenCounts(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("beads open counts: %w", err))
	} else {
		snap.OpenByTypePriority = counts
	}

	// --- Group 2: recent closed latencies ---
	latencies, err := readBeadsLatencies(ctx, db, window)
	if err != nil {
		errs = append(errs, fmt.Errorf("beads latencies: %w", err))
	} else {
		snap.RecentLatencies = latencies
	}

	if len(errs) > 0 {
		combined := errs[0]
		for _, e := range errs[1:] {
			combined = fmt.Errorf("%w; %w", combined, e)
		}
		return snap, combined
	}
	return snap, nil
}

// readBeadsOpenCounts queries the count of open and in_progress issues
// grouped by (type, priority).
func readBeadsOpenCounts(ctx context.Context, db *sql.DB) (map[BeadsKey]int64, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT type, priority, COUNT(*)
		   FROM bd_issues
		  WHERE status IN ('open', 'in_progress')
		  GROUP BY type, priority`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[BeadsKey]int64)
	for rows.Next() {
		var key BeadsKey
		var count int64
		if err := rows.Scan(&key.Type, &key.Priority, &count); err != nil {
			return nil, err
		}
		counts[key] = count
	}
	return counts, rows.Err()
}

// readBeadsLatencies queries issues closed within window and returns one
// LatencySample per issue containing the filing-to-closing duration.
func readBeadsLatencies(ctx context.Context, db *sql.DB, window time.Duration) ([]LatencySample, error) {
	windowSeconds := int64(window.Seconds())
	rows, err := db.QueryContext(ctx,
		`SELECT type, TIMESTAMPDIFF(SECOND, created_at, closed_at)
		   FROM bd_issues
		  WHERE status = 'closed'
		    AND closed_at > NOW() - INTERVAL ? SECOND`,
		windowSeconds,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples []LatencySample
	for rows.Next() {
		var s LatencySample
		var rawSeconds sql.NullInt64
		if err := rows.Scan(&s.Type, &rawSeconds); err != nil {
			return nil, err
		}
		if rawSeconds.Valid && rawSeconds.Int64 >= 0 {
			s.Seconds = float64(rawSeconds.Int64)
			samples = append(samples, s)
		}
	}
	return samples, rows.Err()
}

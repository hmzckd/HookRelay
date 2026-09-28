package telemetry

import (
	"database/sql"
	"fmt"
	"io"
	"strings"
)

// Snapshot contains database-backed, point-in-time measurements. Stored counts
// are gauges because retention can remove historical rows.
type Snapshot struct {
	EventsStored              int64
	Deliveries                [6]int64 // pending, processing, retry_wait, succeeded, failed, dead
	Attempts                  [4]int64 // processing, succeeded, failed, unknown
	RetriesStarted            int64
	QueueReady                int64
	QueueOldestAgeSeconds     float64
	AttemptDurationBuckets    [5]int64 // <= 0.1, 0.5, 1, 2, 5 seconds
	AttemptDurationCount      int64
	AttemptDurationSumSeconds float64
	DBPool                    sql.DBStats
}

func WritePrometheus(w io.Writer, s Snapshot) error {
	var out strings.Builder
	fmt.Fprintf(&out, "# HELP hookrelay_events_stored Events currently retained in PostgreSQL.\n# TYPE hookrelay_events_stored gauge\nhookrelay_events_stored %d\n", s.EventsStored)
	out.WriteString("# HELP hookrelay_deliveries Deliveries currently retained by status.\n# TYPE hookrelay_deliveries gauge\n")
	for i, status := range [...]string{"pending", "processing", "retry_wait", "succeeded", "failed", "dead"} {
		fmt.Fprintf(&out, "hookrelay_deliveries{status=%q} %d\n", status, s.Deliveries[i])
	}
	out.WriteString("# HELP hookrelay_attempts Attempts currently retained by status.\n# TYPE hookrelay_attempts gauge\n")
	for i, status := range [...]string{"processing", "succeeded", "failed", "unknown"} {
		fmt.Fprintf(&out, "hookrelay_attempts{status=%q} %d\n", status, s.Attempts[i])
	}
	fmt.Fprintf(&out, "# HELP hookrelay_retries_started Extra delivery attempts beyond the first, including lease recovery.\n# TYPE hookrelay_retries_started gauge\nhookrelay_retries_started %d\n", s.RetriesStarted)
	fmt.Fprintf(&out, "# HELP hookrelay_queue_ready Deliveries due now in pending or retry_wait.\n# TYPE hookrelay_queue_ready gauge\nhookrelay_queue_ready %d\n", s.QueueReady)
	fmt.Fprintf(&out, "# HELP hookrelay_queue_oldest_age_seconds Age of the oldest waiting delivery, including not-yet-due retries.\n# TYPE hookrelay_queue_oldest_age_seconds gauge\nhookrelay_queue_oldest_age_seconds %g\n", s.QueueOldestAgeSeconds)
	out.WriteString("# HELP hookrelay_attempt_duration_le Completed retained attempts taking at most the stated seconds.\n# TYPE hookrelay_attempt_duration_le gauge\n")
	for i, bound := range [...]string{"0.1", "0.5", "1", "2", "5"} {
		fmt.Fprintf(&out, "hookrelay_attempt_duration_le{seconds=%q} %d\n", bound, s.AttemptDurationBuckets[i])
	}
	fmt.Fprintf(&out, "# HELP hookrelay_attempt_duration_count Completed retained attempts with a measured duration.\n# TYPE hookrelay_attempt_duration_count gauge\nhookrelay_attempt_duration_count %d\n", s.AttemptDurationCount)
	fmt.Fprintf(&out, "# HELP hookrelay_attempt_duration_sum_seconds Sum of retained completed attempt durations in seconds.\n# TYPE hookrelay_attempt_duration_sum_seconds gauge\nhookrelay_attempt_duration_sum_seconds %g\n", s.AttemptDurationSumSeconds)
	out.WriteString("# HELP hookrelay_db_pool_connections API database connections by state.\n# TYPE hookrelay_db_pool_connections gauge\n")
	for _, pool := range []struct {
		state string
		count int
	}{{"open", s.DBPool.OpenConnections}, {"in_use", s.DBPool.InUse}, {"idle", s.DBPool.Idle}} {
		fmt.Fprintf(&out, "hookrelay_db_pool_connections{state=%q} %d\n", pool.state, pool.count)
	}
	fmt.Fprintf(&out, "# HELP hookrelay_db_pool_waits_total API database pool waits since process start.\n# TYPE hookrelay_db_pool_waits_total counter\nhookrelay_db_pool_waits_total %d\n", s.DBPool.WaitCount)
	fmt.Fprintf(&out, "# HELP hookrelay_db_pool_wait_duration_seconds_total API database pool wait time since process start.\n# TYPE hookrelay_db_pool_wait_duration_seconds_total counter\nhookrelay_db_pool_wait_duration_seconds_total %g\n", s.DBPool.WaitDuration.Seconds())
	_, err := io.WriteString(w, out.String())
	return err
}

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

const (
	capacityDBSampleInterval = 100 * time.Millisecond
	capacityDBQueryTimeout   = 80 * time.Millisecond
	capacityDBConnectTimeout = 500 * time.Millisecond
)

// These are observations of sessions at successful polling instants, not query
// counts or elapsed wait durations. No SQL text, IDs or user data are collected.
type capacityDBWaitReport struct {
	Scope                    string           `json:"scope"`
	SampleIntervalMS         int64            `json:"sample_interval_ms"`
	QueryTimeoutMS           int64            `json:"query_timeout_ms"`
	ElapsedSeconds           float64          `json:"elapsed_seconds"`
	Polls                    int64            `json:"polls"`
	SampleAttempts           int64            `json:"sample_attempts"`
	SuccessfulSamples        int64            `json:"successful_samples"`
	ConnectAttempts          int64            `json:"connect_attempts"`
	Errors                   int64            `json:"errors"`
	ErrorsByKind             map[string]int64 `json:"errors_by_kind"`
	InterruptedAttempts      int64            `json:"interrupted_attempts"`
	QuerySeconds             float64          `json:"query_seconds"`
	MaxSampleGapMS           float64          `json:"max_sample_gap_ms"`
	SessionSamples           int64            `json:"session_samples"`
	StateSessionSamples      map[string]int64 `json:"state_session_samples"`
	WaitTypeSessionSamples   map[string]int64 `json:"wait_event_type_session_samples"`
	WaitSessionSamples       map[string]int64 `json:"wait_event_session_samples"`
	ActivitySessionSamples   map[string]int64 `json:"state_wait_event_session_samples"`
	LockWaitSessionSamples   int64            `json:"lock_wait_session_samples"`
	BlockedSessionSamples    int64            `json:"blocked_session_samples"`
	MaxTransactionAgeSeconds float64          `json:"max_transaction_age_seconds"`
}

type capacityDBActivity struct {
	state, waitType, waitEvent string
	transactionAge             float64
	blocked                    bool
}

func newCapacityDBWaitReport() capacityDBWaitReport {
	return capacityDBWaitReport{
		Scope:            "current-database sessions excluding collector; counts are session samples, not wait durations; unknown means unavailable",
		SampleIntervalMS: capacityDBSampleInterval.Milliseconds(), QueryTimeoutMS: capacityDBQueryTimeout.Milliseconds(),
		ErrorsByKind: map[string]int64{}, StateSessionSamples: map[string]int64{}, WaitTypeSessionSamples: map[string]int64{},
		WaitSessionSamples: map[string]int64{}, ActivitySessionSamples: map[string]int64{},
	}
}

func (r *capacityDBWaitReport) add(rows []capacityDBActivity) {
	r.SuccessfulSamples++
	for _, row := range rows {
		r.SessionSamples++
		r.StateSessionSamples[row.state]++
		r.WaitTypeSessionSamples[row.waitType]++
		r.WaitSessionSamples[row.waitType+"/"+row.waitEvent]++
		r.ActivitySessionSamples[row.state+"/"+row.waitType+"/"+row.waitEvent]++
		if row.waitType == "Lock" {
			r.LockWaitSessionSamples++
		}
		if row.blocked {
			r.BlockedSessionSamples++
		}
		if row.transactionAge > r.MaxTransactionAgeSeconds {
			r.MaxTransactionAgeSeconds = row.transactionAge
		}
	}
}

// Autocommit gives every poll a fresh activity snapshot. Check blocker PIDs
// only for lock waiters, and retain only whether a blocker exists.
const capacityDBActivitySQL = `SELECT COALESCE(state,'unknown'),
 COALESCE(wait_event_type,'none'), COALESCE(wait_event,'none'),
 GREATEST(COALESCE(EXTRACT(EPOCH FROM clock_timestamp()-xact_start),0),0)::double precision,
 CASE WHEN wait_event_type='Lock' THEN cardinality(pg_blocking_pids(pid))>0 ELSE false END
 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`

func sampleCapacityDBActivity(ctx context.Context, conn *pgx.Conn) ([]capacityDBActivity, error) {
	rows, err := conn.Query(ctx, capacityDBActivitySQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []capacityDBActivity
	for rows.Next() {
		var row capacityDBActivity
		if err = rows.Scan(&row.state, &row.waitType, &row.waitEvent, &row.transactionAge, &row.blocked); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type capacityDBDiagnostics struct {
	mu     sync.Mutex
	report capacityDBWaitReport
	cancel context.CancelFunc
	done   chan struct{}
}

func startCapacityDBDiagnostics(parent context.Context, config *pgx.ConnConfig, started time.Time, duration time.Duration) *capacityDBDiagnostics {
	ctx, cancel := context.WithDeadline(parent, started.Add(duration))
	d := &capacityDBDiagnostics{report: newCapacityDBWaitReport(), cancel: cancel, done: make(chan struct{})}
	config = config.Copy()
	config.Tracer = nil // Diagnostic traffic is not application query traffic.
	config.RuntimeParams["application_name"] = "capacity_wait_diagnostics"
	go d.run(ctx, config, started)
	return d
}

func closeCapacityDBConnection(conn *pgx.Conn) {
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), capacityDBConnectTimeout)
		defer cancel()
		_ = conn.Close(ctx)
		// A query timeout can mark pgx closed before its asynchronous cleanup
		// finishes. Do not reconnect or finish the collector with that socket
		// still open. CleanupDone and Conn are public pgconn lifecycle APIs;
		// the driver's cancellation cleanup has its own 15-second deadline.
		select {
		case <-conn.PgConn().CleanupDone():
		default:
			_ = conn.PgConn().Conn().Close()
			<-conn.PgConn().CleanupDone()
		}
	}
}

func capacityDBErrorKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "sqlstate_" + pgErr.Code
	}
	return "connection_or_query_error"
}

func (d *capacityDBDiagnostics) run(ctx context.Context, config *pgx.ConnConfig, started time.Time) {
	var conn *pgx.Conn
	lastSample, nextConnect := started, started
	defer func() {
		ended := time.Now()
		closeCapacityDBConnection(conn)
		d.mu.Lock()
		d.report.ElapsedSeconds = ended.Sub(started).Seconds()
		d.report.MaxSampleGapMS = max(d.report.MaxSampleGapMS, float64(ended.Sub(lastSample))/float64(time.Millisecond))
		d.mu.Unlock()
		close(d.done)
	}()
	// One goroutine, no overlapping queries or catch-up polls under overload.
	ticker := time.NewTicker(capacityDBSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		d.mu.Lock()
		d.report.Polls++
		d.mu.Unlock()
		if conn == nil {
			if time.Now().Before(nextConnect) {
				continue
			}
			d.mu.Lock()
			d.report.ConnectAttempts++
			d.mu.Unlock()
			connectCtx, cancel := context.WithTimeout(ctx, capacityDBConnectTimeout)
			var err error
			conn, err = pgx.ConnectConfig(connectCtx, config)
			cancel()
			if err != nil {
				d.recordError(ctx, "connect", err)
				nextConnect = time.Now().Add(time.Second)
				continue
			}
		}
		queryCtx, cancel := context.WithTimeout(ctx, capacityDBQueryTimeout)
		d.mu.Lock()
		d.report.SampleAttempts++
		d.mu.Unlock()
		queryStarted := time.Now()
		rows, err := sampleCapacityDBActivity(queryCtx, conn)
		queryEnded := time.Now()
		cancel()
		d.mu.Lock()
		d.report.QuerySeconds += queryEnded.Sub(queryStarted).Seconds()
		if err == nil && ctx.Err() == nil {
			d.report.add(rows)
			d.report.MaxSampleGapMS = max(d.report.MaxSampleGapMS, float64(queryEnded.Sub(lastSample))/float64(time.Millisecond))
			lastSample = queryEnded
		}
		d.mu.Unlock()
		if ctx.Err() != nil {
			d.recordError(ctx, "query", ctx.Err())
			return
		}
		if err != nil {
			d.recordError(ctx, "query", err)
			closeCapacityDBConnection(conn)
			conn = nil
			nextConnect = time.Now().Add(time.Second)
		}
	}
}

func (d *capacityDBDiagnostics) recordError(ctx context.Context, operation string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ctx.Err() != nil {
		d.report.InterruptedAttempts++
		return
	}
	d.report.Errors++
	d.report.ErrorsByKind[operation+"/"+capacityDBErrorKind(err)]++
}

func (d *capacityDBDiagnostics) stop() capacityDBWaitReport {
	d.cancel()
	<-d.done
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report // The joined collector can no longer mutate these maps.
}

func (d *capacityDBDiagnostics) progress() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var waits []string
	for key := range d.report.WaitSessionSamples {
		if !strings.HasPrefix(key, "Client/") && key != "none/none" {
			waits = append(waits, key)
		}
	}
	sort.Slice(waits, func(i, j int) bool {
		a, b := d.report.WaitSessionSamples[waits[i]], d.report.WaitSessionSamples[waits[j]]
		return a > b || a == b && waits[i] < waits[j]
	})
	if len(waits) > 3 {
		waits = waits[:3]
	}
	for i, wait := range waits {
		waits[i] = fmt.Sprintf("%s=%d", wait, d.report.WaitSessionSamples[wait])
	}
	return fmt.Sprintf("samples=%d/%d errors=%d blocked_session_samples=%d top_wait_session_samples=[%s]", d.report.SuccessfulSamples, d.report.SampleAttempts, d.report.Errors, d.report.BlockedSessionSamples, strings.Join(waits, ","))
}

func TestCapacityDBWaitAggregation(t *testing.T) {
	r := newCapacityDBWaitReport()
	r.add([]capacityDBActivity{{"active", "Lock", "transactionid", 3.5, true}, {"idle", "Client", "ClientRead", 0, false}})
	r.add([]capacityDBActivity{{"active", "IO", "WALSync", 0.5, false}, {"active", "Lock", "transactionid", 2, true}, {"unknown", "none", "none", 0, false}})
	if r.SuccessfulSamples != 2 || r.SessionSamples != 5 || r.StateSessionSamples["active"] != 3 || r.WaitTypeSessionSamples["Lock"] != 2 || r.WaitSessionSamples["IO/WALSync"] != 1 || r.ActivitySessionSamples["active/Lock/transactionid"] != 2 || r.BlockedSessionSamples != 2 || r.LockWaitSessionSamples != 2 || r.MaxTransactionAgeSeconds != 3.5 || r.StateSessionSamples["unknown"] != 1 {
		t.Fatalf("incorrect session-sample aggregation: %+v", r)
	}
	d := &capacityDBDiagnostics{report: r}
	d.recordError(context.Background(), "query", &pgconn.PgError{Code: "XX000", Message: "sensitive query payload"})
	if d.report.ErrorsByKind["query/sqlstate_XX000"] != 1 || strings.Contains(d.progress(), "payload") {
		t.Fatalf("diagnostic error classification: %+v", d.report.ErrorsByKind)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.recordError(ctx, "query", context.Canceled)
	if d.report.Errors != 1 || d.report.InterruptedAttempts != 1 {
		t.Fatalf("normal stop counted as collector failure: %+v", d.report)
	}
}

func TestCapacityDBDiagnosticsFailureIsBoundedAndReported(t *testing.T) {
	config, err := pgx.ParseConfig("host=127.0.0.1 user=capacity_diagnostic dbname=capacity_diagnostic sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("sensitive connection detail")
	}
	d := startCapacityDBDiagnostics(context.Background(), config, time.Now(), 250*time.Millisecond)
	defer d.stop()
	select {
	case <-d.done:
	case <-time.After(3 * time.Second):
		t.Fatal("collector failed to finish at its measurement deadline")
	}
	r := d.stop()
	if r.ConnectAttempts != 1 || r.Errors != 1 || r.ErrorsByKind["connect/connection_or_query_error"] != 1 || r.SuccessfulSamples != 0 || r.SampleAttempts != 0 || r.ElapsedSeconds < .2 || r.ElapsedSeconds > 2 || r.InterruptedAttempts != 0 {
		t.Fatalf("unavailable diagnostics were not reported accurately: %+v", r)
	}
	encoded, err := json.Marshal(r)
	if err != nil || strings.Contains(string(encoded), "sensitive") {
		t.Fatalf("collector exposed connection detail: %s %v", encoded, err)
	}
}

// A real transaction row lock must remain visible even when the application
// pool has no free slots. This is not a mock of PostgreSQL wait-event names.
func TestCapacityDBDiagnosticsObservesRowLockWithExhaustedPool(t *testing.T) {
	s := recoveryService(t, mcr.Rule{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `CREATE TABLE capacity_diagnostic_lock(id int PRIMARY KEY, value int); INSERT INTO capacity_diagnostic_lock VALUES(1,0)`); err != nil {
		t.Fatal(err)
	}
	waiter, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCapacityDBConnection(waiter)
	var held []*pgxpool.Conn
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()
	for range int(s.pool.Config().MaxConns) {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	tx, err := held[0].Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `UPDATE capacity_diagnostic_lock SET value=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	waitDone := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(waitCtx, `UPDATE capacity_diagnostic_lock SET value=2 WHERE id=1`)
		waitDone <- err
	}()
	defer func() { cancelWait(); <-waitDone }()
	baseline := s.pool.Stat().AcquireCount()
	d := startCapacityDBDiagnostics(ctx, s.pool.Config().ConnConfig, time.Now(), 3*time.Second)
	defer d.stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		d.mu.Lock()
		observed := d.report.WaitSessionSamples["Lock/transactionid"] > 0 && d.report.BlockedSessionSamples > 0
		d.mu.Unlock()
		if observed {
			break
		}
		select {
		case <-ticker.C:
		case <-d.done:
			t.Fatalf("retained row lock was not sampled: %+v", d.stop())
		}
	}
	r := d.stop()
	if r.Errors != 0 || r.SuccessfulSamples == 0 || r.MaxTransactionAgeSeconds <= 0 || r.ElapsedSeconds <= 0 || r.QuerySeconds <= 0 {
		t.Fatalf("invalid collector coverage: %+v", r)
	}
	if got := s.pool.Stat(); got.AcquireCount() != baseline || got.AcquiredConns() != got.MaxConns() {
		t.Fatalf("diagnostics used an application pool slot: before=%d after=%d held=%d/%d", baseline, got.AcquireCount(), got.AcquiredConns(), got.MaxConns())
	}
}

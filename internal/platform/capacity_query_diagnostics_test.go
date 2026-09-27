package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

var capacityTimingBounds = [...]time.Duration{time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond}
var capacityTimingNames = [...]string{"less_than_1_ms", "1_to_2_ms", "2_to_5_ms", "5_to_10_ms", "10_to_25_ms", "25_to_50_ms", "50_to_100_ms", "100_to_250_ms", "250_to_500_ms", "at_least_500_ms"}

type capacityTimingHistogram struct {
	Count        int64     `json:"count"`
	Errors       int64     `json:"errors"`
	TotalMS      float64   `json:"total_ms"`
	MaxMS        float64   `json:"max_ms"`
	AtLeast100MS int64     `json:"at_least_100_ms"`
	Buckets      [10]int64 `json:"buckets"`
}

func (h *capacityTimingHistogram) add(elapsed time.Duration, failed bool) {
	h.Count++
	if failed {
		h.Errors++
	}
	ms := float64(elapsed) / float64(time.Millisecond)
	h.TotalMS += ms
	h.MaxMS = max(h.MaxMS, ms)
	if elapsed >= 100*time.Millisecond {
		h.AtLeast100MS++
	}
	bucket := len(capacityTimingBounds)
	for i, bound := range capacityTimingBounds {
		if elapsed < bound {
			bucket = i
			break
		}
	}
	h.Buckets[bucket]++
}

type capacityQueryTimingReport struct {
	Scope       string                             `json:"scope"`
	BucketNames [10]string                         `json:"bucket_names"`
	Operations  map[string]capacityTimingHistogram `json:"operations"`
}

const (
	capacitySQLRead = iota
	capacitySQLBegin
	capacitySQLCommit
	capacitySQLRollback
	capacitySQLTimerLock
	capacitySQLCommandLock
	capacitySQLWrite
	capacitySQLLease
)

// Match only the application's fixed SQL shapes. SQL and arguments are never
// retained, normalized into labels, or included in diagnostic errors.
func capacitySQLKind(sql string) int {
	sql = strings.TrimSpace(sql)
	switch sql {
	case "begin", "BEGIN":
		return capacitySQLBegin
	case "commit", "COMMIT":
		return capacitySQLCommit
	case "rollback", "ROLLBACK":
		return capacitySQLRollback
	}
	if strings.HasPrefix(sql, "WITH renewable AS (") {
		return capacitySQLLease
	}
	if strings.HasPrefix(sql, "SELECT ") && strings.Contains(sql, " FROM platform_matches ") {
		if strings.HasSuffix(sql, " FOR UPDATE SKIP LOCKED") {
			return capacitySQLTimerLock
		}
		if strings.HasSuffix(sql, " FOR UPDATE") {
			return capacitySQLCommandLock
		}
	}
	if strings.HasPrefix(sql, "INSERT ") || strings.HasPrefix(sql, "UPDATE ") || strings.HasPrefix(sql, "DELETE ") {
		return capacitySQLWrite
	}
	return capacitySQLRead
}

type capacityTimingTrace struct {
	started    time.Time
	generation uint64
	kind       int
	operation  string
	finished   bool // Batch errors may produce both early and Close callbacks.
}
type capacityTransactionTrace struct {
	started    time.Time
	generation uint64
	role       string
}
type capacityQueryTraceKey struct{}
type capacityAcquireTraceKey struct{}
type capacityBatchTraceKey struct{}

type capacityQueryDiagnostics struct {
	mu           sync.Mutex
	measuring    bool
	generation   uint64
	transactions map[*pgx.Conn]capacityTransactionTrace
	operations   map[string]capacityTimingHistogram
}

func newCapacityQueryDiagnostics() *capacityQueryDiagnostics {
	return &capacityQueryDiagnostics{transactions: map[*pgx.Conn]capacityTransactionTrace{}, operations: map[string]capacityTimingHistogram{}}
}

func capacityInstallQueryDiagnostics(t *testing.T, s *Service) *capacityQueryDiagnostics {
	t.Helper()
	d := newCapacityQueryDiagnostics()
	config := s.pool.Config()
	config.ConnConfig.Tracer = d
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	old := s.pool
	s.pool = pool // Called before starting server, workers, or load generators.
	old.Close()
	t.Cleanup(pool.Close)
	return d
}

func (d *capacityQueryDiagnostics) start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.generation++
	d.measuring = true
	d.operations = map[string]capacityTimingHistogram{}
}

func (d *capacityQueryDiagnostics) stop() capacityQueryTimingReport {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.measuring = false
	ops := make(map[string]capacityTimingHistogram, len(d.operations))
	for key, value := range d.operations {
		ops[key] = value
	}
	return capacityQueryTimingReport{Scope: "operations starting and finishing during measurement; timer transactions run from BEGIN start through COMMIT/ROLLBACK end and exclude pool acquisition; query/batch durations include client round trips and row consumption, not just PostgreSQL execution; timer role is the match SELECT FOR UPDATE SKIP LOCKED shape; fixed histogram counts do not imply exact percentiles", BucketNames: capacityTimingNames, Operations: ops}
}

func (d *capacityQueryDiagnostics) generationLocked() uint64 {
	if d.measuring {
		return d.generation
	}
	return 0
}

func (d *capacityQueryDiagnostics) recordLocked(trace capacityTimingTrace, ended time.Time, failed bool) {
	if trace.operation == "" || !d.measuring || trace.generation == 0 || trace.generation != d.generation {
		return
	}
	h := d.operations[trace.operation]
	h.add(ended.Sub(trace.started), failed)
	d.operations[trace.operation] = h
}

func (d *capacityQueryDiagnostics) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	trace := capacityTimingTrace{started: time.Now(), kind: capacitySQLKind(data.SQL)}
	d.mu.Lock()
	trace.generation = d.generationLocked()
	tx := d.transactions[conn]
	switch trace.kind {
	case capacitySQLBegin:
		d.transactions[conn] = capacityTransactionTrace{started: trace.started, generation: trace.generation}
	case capacitySQLTimerLock:
		tx.role = "timer"
		d.transactions[conn] = tx
		trace.operation = "timer_match_row_lock"
	case capacitySQLCommandLock:
		tx.role = "command_or_admin"
		d.transactions[conn] = tx
	case capacitySQLLease:
		trace.operation = "lease_update"
	case capacitySQLCommit:
		trace.operation = "other_commit"
		if tx.role == "timer" {
			trace.operation = "timer_commit"
		}
	case capacitySQLRollback:
		if tx.role == "timer" {
			trace.operation = "timer_rollback"
		}
	default:
		if tx.role == "timer" {
			trace.operation = "timer_other_query"
			if trace.kind == capacitySQLWrite {
				trace.operation = "timer_write_or_batch"
			}
		}
	}
	d.mu.Unlock()
	return context.WithValue(ctx, capacityQueryTraceKey{}, trace)
}

func (d *capacityQueryDiagnostics) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(capacityQueryTraceKey{}).(capacityTimingTrace)
	if !ok {
		return
	}
	ended := time.Now()
	failed := data.Err != nil || trace.kind == capacitySQLCommit && data.CommandTag.String() == "ROLLBACK"
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recordLocked(trace, ended, failed)
	if trace.kind == capacitySQLCommit || trace.kind == capacitySQLRollback {
		tx := d.transactions[conn]
		if tx.role == "timer" && !tx.started.IsZero() {
			d.recordLocked(capacityTimingTrace{started: tx.started, generation: tx.generation, operation: "timer_total_transaction"}, ended, failed)
		}
		delete(d.transactions, conn)
	} else if trace.kind == capacitySQLBegin && data.Err != nil {
		delete(d.transactions, conn)
	}
}

func (d *capacityQueryDiagnostics) TraceBatchStart(ctx context.Context, conn *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	trace := capacityTimingTrace{started: time.Now()}
	d.mu.Lock()
	trace.generation = d.generationLocked()
	if d.transactions[conn].role == "timer" {
		trace.operation = "timer_write_or_batch"
	}
	d.mu.Unlock()
	return context.WithValue(ctx, capacityBatchTraceKey{}, &trace)
}
func (*capacityQueryDiagnostics) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
}
func (d *capacityQueryDiagnostics) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	trace, ok := ctx.Value(capacityBatchTraceKey{}).(*capacityTimingTrace)
	if ok {
		d.mu.Lock()
		if !trace.finished {
			d.recordLocked(*trace, time.Now(), data.Err != nil)
			trace.finished = true
		}
		d.mu.Unlock()
	}
}
func (d *capacityQueryDiagnostics) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	d.mu.Lock()
	trace := capacityTimingTrace{started: time.Now(), generation: d.generationLocked(), operation: "pool_acquire"}
	d.mu.Unlock()
	return context.WithValue(ctx, capacityAcquireTraceKey{}, trace)
}
func (d *capacityQueryDiagnostics) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	trace, ok := ctx.Value(capacityAcquireTraceKey{}).(capacityTimingTrace)
	if ok {
		d.mu.Lock()
		d.recordLocked(trace, time.Now(), data.Err != nil)
		d.mu.Unlock()
	}
}
func (d *capacityQueryDiagnostics) TraceRelease(_ *pgxpool.Pool, data pgxpool.TraceReleaseData) {
	d.mu.Lock()
	delete(d.transactions, data.Conn)
	d.mu.Unlock()
}

func TestCapacityQueryTimingHistogramAndPrivacy(t *testing.T) {
	var h capacityTimingHistogram
	for _, elapsed := range []time.Duration{0, time.Millisecond, 100 * time.Millisecond, 500 * time.Millisecond} {
		h.add(elapsed, elapsed == 500*time.Millisecond)
	}
	if h.Count != 4 || h.Errors != 1 || h.TotalMS != 601 || h.MaxMS != 500 || h.AtLeast100MS != 2 || h.Buckets[0] != 1 || h.Buckets[1] != 1 || h.Buckets[7] != 1 || h.Buckets[9] != 1 {
		t.Fatalf("fixed histogram: %+v", h)
	}
	d := newCapacityQueryDiagnostics()
	d.start()
	conn := &pgx.Conn{}
	query := func(sql string) {
		ctx := d.TraceQueryStart(context.Background(), conn, pgx.TraceQueryStartData{SQL: sql, Args: []any{"secret-user-payload"}})
		d.TraceQueryEnd(ctx, conn, pgx.TraceQueryEndData{})
	}
	query("begin")
	query("SELECT id FROM platform_matches WHERE id=$1 FOR UPDATE SKIP LOCKED")
	query("UPDATE platform_matches SET choices=$1 WHERE id=$2")
	batchContext := d.TraceBatchStart(context.Background(), conn, pgx.TraceBatchStartData{})
	d.TraceBatchEnd(batchContext, conn, pgx.TraceBatchEndData{Err: errors.New("private error detail")})
	d.TraceBatchEnd(batchContext, conn, pgx.TraceBatchEndData{Err: errors.New("private error detail")})
	query("commit")
	query("begin")
	query("SELECT id FROM platform_matches WHERE id=$1 FOR UPDATE")
	query("commit")
	query("WITH renewable AS (SELECT id FROM platform_matches FOR UPDATE SKIP LOCKED) UPDATE platform_matches SET owner_until=now()")
	r := d.stop()
	for _, op := range []string{"timer_match_row_lock", "timer_commit", "timer_total_transaction", "other_commit", "lease_update"} {
		if r.Operations[op].Count != 1 {
			t.Fatalf("missing fixed operation %s: %+v", op, r)
		}
	}
	if got := r.Operations["timer_write_or_batch"]; got.Count != 2 || got.Errors != 1 {
		t.Fatalf("batch double-counted or error lost: %+v", got)
	}
	encoded, err := json.Marshal(r)
	if err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "choices") {
		t.Fatalf("private SQL or payload retained: %s %v", encoded, err)
	}
	query("commit")
	if d.stop().Operations["other_commit"].Count != 1 {
		t.Fatal("post-measurement operation was recorded")
	}
	d.start()
	query("begin")
	d.stop()
	d.start()
	query("SELECT id FROM platform_matches WHERE id=$1 FOR UPDATE SKIP LOCKED")
	query("commit")
	if d.stop().Operations["timer_total_transaction"].Count != 0 {
		t.Fatal("transaction crossing measurement boundary was recorded")
	}
}

func TestCapacityQueryDiagnosticsRealPostgres(t *testing.T) {
	s := recoveryService(t, mcr.New())
	d := capacityInstallQueryDiagnostics(t, s)
	d.start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var mid string
	err = tx.QueryRow(ctx, `SELECT id FROM platform_matches WHERE id=$1 FOR UPDATE SKIP LOCKED`, "private-test-id").Scan(&mid)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("empty fixture match lookup: %v", err)
	}
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE platform_matches SET choices=choices WHERE id=$1`, "private-test-id")
	if err = tx.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := d.stop()
	for _, op := range []string{"pool_acquire", "timer_match_row_lock", "timer_write_or_batch", "timer_commit", "timer_total_transaction"} {
		if got := r.Operations[op]; got.Count != 1 || got.Errors != 0 || got.TotalMS <= 0 {
			t.Fatalf("missing real PostgreSQL operation %s: %+v", op, got)
		}
	}
}

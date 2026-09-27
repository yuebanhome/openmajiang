package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Count only query kinds. Never retain credential arguments or SQL parameter values.
type activityQueries struct {
	reads   atomic.Int64
	touches atomic.Int64
}

func (q *activityQueries) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT s.id,s.csrf_token") {
		q.reads.Add(1)
	}
	if strings.HasPrefix(data.SQL, "UPDATE auth_sessions SET last_seen_at") {
		q.touches.Add(1)
	}
	return ctx
}
func (*activityQueries) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestSessionActivityThrottleKeepsLiveRevocationPostgres(t *testing.T) {
	queries := &activityQueries{}
	s, mux, _ := integrationService(t, queries)
	ctx := context.Background()
	expectStatus(t, call(t, mux, "POST", "/v1/auth/register", map[string]any{
		"email": "activity@example.test", "password": "long activity test password", "name": "Activity", "accept_terms": true,
	}, nil, ""), 202)
	cookie, _ := loginTest(t, mux, "activity@example.test", "long activity test password")
	r := httptest.NewRequest("GET", "http://localhost:8080/v1/me", nil)
	r.AddCookie(cookie)
	for i := 0; i < 2; i++ {
		if _, err := s.Authenticate(r); err != nil {
			t.Fatal(err)
		}
	}
	if queries.reads.Load() != 2 || queries.touches.Load() != 0 {
		t.Fatalf("fresh activity needs two live reads and no writes: reads=%d writes=%d", queries.reads.Load(), queries.touches.Load())
	}
	if _, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at=now()-interval '6 minutes'`); err != nil {
		t.Fatal(err)
	}
	queries.touches.Store(0)
	for i := 0; i < 2; i++ {
		if _, err := s.Authenticate(r); err != nil {
			t.Fatal(err)
		}
	}
	if queries.reads.Load() != 4 || queries.touches.Load() != 1 {
		t.Fatalf("due activity must refresh once while still rechecking each request: reads=%d writes=%d", queries.reads.Load(), queries.touches.Load())
	}
	if _, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(r); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a just-revoked session must fail immediately: %v", err)
	}
	if queries.reads.Load() != 5 || queries.touches.Load() != 1 {
		t.Fatal("revoked session must still perform a live read and must not refresh activity")
	}
}

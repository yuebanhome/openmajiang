package database

import (
	"context"
	"testing"
)

func TestParseConfigDefaultAndExplicitPoolLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		dsn  string
		want int32
	}{
		{"url default", "postgres://localhost/openmajiang?sslmode=disable", 8},
		{"keyword default", "host=localhost dbname=openmajiang sslmode=disable", 8},
		{"explicit lower", "postgres://localhost/openmajiang?sslmode=disable&pool_max_conns=3", 3},
		{"explicit higher", "postgres://localhost/openmajiang?sslmode=disable&pool_max_conns=12", 12},
		{"explicit keyword", "host=localhost dbname=openmajiang sslmode=disable pool_max_conns=6", 6},
		{"option text in password", "host=localhost dbname=openmajiang sslmode=disable password='pool_max_conns=99'", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig(tc.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConns != tc.want {
				t.Fatalf("pool maximum = %d, want %d", cfg.MaxConns, tc.want)
			}
			if _, leaked := cfg.ConnConfig.RuntimeParams["pool_max_conns"]; leaked {
				t.Fatal("pool option would be sent to PostgreSQL as a session parameter")
			}
		})
	}
}

func TestInvalidPoolLimitsFailBeforePoolCreation(t *testing.T) {
	for _, value := range []string{"0", "-1", "invalid", "2147483648"} {
		t.Run(value, func(t *testing.T) {
			pool, err := New(context.Background(), "postgres://localhost/openmajiang?sslmode=disable&pool_max_conns="+value)
			if pool != nil {
				pool.Close()
				t.Fatal("invalid limit created a pool")
			}
			if err == nil {
				t.Fatal("invalid limit was accepted")
			}
		})
	}
}

func TestParseConfigPreservesPostgresSessionOptions(t *testing.T) {
	cfg, err := ParseConfig("postgres://localhost/openmajiang?sslmode=disable&search_path=isolated_test")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.RuntimeParams["search_path"] != "isolated_test" {
		t.Fatal("application defaults changed the PostgreSQL session configuration")
	}
}

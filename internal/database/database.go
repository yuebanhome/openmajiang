package database

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns bounds the application's shared read/write pool. The server
// and real PostgreSQL integration gates must use this same configuration.
const DefaultMaxConns int32 = 8

func ParseConfig(connectionString string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, err
	}
	// pgxpool removes its options from RuntimeParams. Parse the original input
	// with pgconn to detect an explicit option without substring matching or
	// restricting the supported PostgreSQL URL/keyword connection formats.
	source, err := pgconn.ParseConfig(connectionString)
	if err != nil {
		return nil, err
	}
	if _, explicit := source.RuntimeParams["pool_max_conns"]; !explicit {
		cfg.MaxConns = DefaultMaxConns
	}
	return cfg, nil
}

func New(ctx context.Context, connectionString string) (*pgxpool.Pool, error) {
	cfg, err := ParseConfig(connectionString)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

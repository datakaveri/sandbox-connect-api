package db

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgPool struct {
	Pool *pgxpool.Pool
}

func NewPool(url string) (*PgPool, error) {
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}

	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	p, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := p.Ping(ctx); err != nil {
		return nil, err
	}

	slog.Info("connected to database", "query_exec_mode", "simple_protocol")
	return &PgPool{
		Pool: p,
	}, nil
}

package db

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
)

type PgPool struct {
	Pool *pgxpool.Pool
}

func NewPool(url string) (*PgPool, error) {
	ctx := context.Background()
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := p.Ping(ctx); err != nil {
		return nil, err
	}
	slog.Info("connected to database")
	return &PgPool{
		Pool: p,
	}, nil
}

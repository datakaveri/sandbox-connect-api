package db

import (
	"context"

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
	p, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := p.Ping(ctx); err != nil {
		return nil, err
	}

	return &PgPool{
		Pool: p,
	}, nil
}

func (p *PgPool) Close() {
	if p.Pool != nil {
		p.Pool.Close()
	}
}

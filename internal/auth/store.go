package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS oauth_clients (
			client_id    TEXT PRIMARY KEY,
			redirect_uris TEXT[] NOT NULL,
			client_name  TEXT,
			token_method TEXT NOT NULL DEFAULT 'none'
		);
		CREATE TABLE IF NOT EXISTS oauth_tokens (
			token      TEXT PRIMARY KEY,
			client_id  TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_tokens_expires ON oauth_tokens (expires_at);
	`)
	return err
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) SaveClient(ctx context.Context, c *clientRegistration) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_clients (client_id, redirect_uris, client_name, token_method)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (client_id) DO UPDATE SET redirect_uris=$2, client_name=$3, token_method=$4`,
		c.ClientID, c.RedirectURIs, c.ClientName, c.TokenMethod)
	return err
}

func (s *Store) LoadClient(ctx context.Context, clientID string) (*clientRegistration, error) {
	var c clientRegistration
	err := s.pool.QueryRow(ctx,
		`SELECT client_id, redirect_uris, client_name, token_method FROM oauth_clients WHERE client_id=$1`,
		clientID).Scan(&c.ClientID, &c.RedirectURIs, &c.ClientName, &c.TokenMethod)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func (s *Store) SaveToken(ctx context.Context, token, clientID string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_tokens (token, client_id, expires_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (token) DO UPDATE SET client_id=$2, expires_at=$3`,
		token, clientID, expiresAt)
	return err
}

func (s *Store) ValidateToken(ctx context.Context, token string) bool {
	var expiresAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT expires_at FROM oauth_tokens WHERE token=$1`, token).Scan(&expiresAt)
	if err != nil {
		return false
	}
	if time.Now().After(expiresAt) {
		s.pool.Exec(ctx, `DELETE FROM oauth_tokens WHERE token=$1`, token)
		return false
	}
	return true
}

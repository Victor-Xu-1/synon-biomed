package transcript

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"time"
)

const maxEventPayloadBytes = 1 << 20

// MaxEventPayloadBytes is the protocol-wide bound for one immutable
// Transcript event. Producers use it to calculate the remaining inline budget
// before committing a side effect.
const MaxEventPayloadBytes = maxEventPayloadBytes

type Repository struct {
	db     *sql.DB
	readDB *sql.DB
	now    func() time.Time
	rand   io.Reader
}

// ImmediateTransaction is the shared SQLite authority used when a workspace
// mutation and transcript mutation must commit or roll back together.
type ImmediateTransaction struct {
	repository *Repository
	conn       *sql.Conn
}

func (tx *ImmediateTransaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.conn.ExecContext(ctx, query, args...)
}

func (tx *ImmediateTransaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.conn.QueryContext(ctx, query, args...)
}

func (tx *ImmediateTransaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.conn.QueryRowContext(ctx, query, args...)
}

func NewRepository(db *sql.DB) *Repository {
	return NewRepositoryWithReadPool(db, nil)
}

// NewRepositoryWithReadPool keeps mutations on the serialized writer pool and
// routes read-only projections through the caller-owned WAL read pool. The
// latter must be query-only; Repository never closes either pool.
func NewRepositoryWithReadPool(writeDB, readDB *sql.DB) *Repository {
	return &Repository{db: writeDB, readDB: readDB, now: time.Now, rand: rand.Reader}
}

func (r *Repository) readDatabase() *sql.DB {
	if r == nil {
		return nil
	}
	if r.readDB != nil {
		return r.readDB
	}
	return r.db
}

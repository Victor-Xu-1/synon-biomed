package workspace

import (
	"context"
	"database/sql"
)

type schemaMigrationExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

var (
	_ schemaMigrationExecutor = (*sql.DB)(nil)
	_ schemaMigrationExecutor = (*sql.Conn)(nil)
)

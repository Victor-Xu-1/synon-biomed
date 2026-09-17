package workspace

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestListModelProvidersWithContextHonorsDeadlineWhenReadPoolIsExhausted(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	poolSize := store.readDB.Stats().MaxOpenConnections
	rows := make([]*sql.Rows, 0, poolSize)
	defer func() {
		for _, row := range rows {
			_ = row.Close()
		}
	}()
	for index := 0; index < poolSize; index++ {
		row, queryErr := store.readDB.QueryContext(context.Background(), "SELECT 1")
		if queryErr != nil {
			t.Fatalf("hold read connection %d: %v", index, queryErr)
		}
		rows = append(rows, row)
	}
	if got := store.readDB.Stats().InUse; got != poolSize {
		t.Fatalf("read connections in use = %d, want %d", got, poolSize)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = store.ListModelProvidersWithContext(ctx, "local")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("provider list error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked provider list returned after %s, want bounded cancellation", elapsed)
	}
}

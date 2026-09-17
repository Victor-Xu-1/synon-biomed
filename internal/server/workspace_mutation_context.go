package server

import (
	"context"
	"net/http"

	workspace "synon-go/internal/persistence/workspace"
)

func workspaceMutationContext(r *http.Request) context.Context {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = r.Header.Get("X-Synon-Idempotency-Key")
	}
	return workspace.WithMutationIdempotencyKey(r.Context(), key)
}

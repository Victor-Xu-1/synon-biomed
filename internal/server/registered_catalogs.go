package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"synon-go/internal/tools/registry"
)

// registeredTool resolves a schema across the two disjoint authorities. The
// model catalog is checked first; service operations never enter model
// snapshots unless a caller explicitly requests that exact compatibility
// identity.
func (s *Server) registeredTool(name string) (registry.Tool, bool) {
	if s == nil {
		return registry.Tool{}, false
	}
	if s.tools != nil {
		if tool, ok := s.tools.Get(name); ok {
			return tool, true
		}
	}
	if s.operations != nil {
		return s.operations.Get(name)
	}
	return registry.Tool{}, false
}

func (s *Server) validateRegisteredTool(name string, input map[string]any) error {
	if s == nil {
		return fmt.Errorf("unknown tool: %s", name)
	}
	if s.tools != nil {
		if _, ok := s.tools.Get(name); ok {
			return s.tools.Validate(name, input)
		}
	}
	if s.operations != nil {
		if _, ok := s.operations.Get(name); ok {
			return s.operations.Validate(name, input)
		}
	}
	return fmt.Errorf("unknown tool: %s", name)
}

func (s *Server) executeRegisteredTool(ctx context.Context, name string, input map[string]any) (any, error) {
	if s == nil {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	if s.tools != nil {
		if _, ok := s.tools.Get(name); ok {
			return s.tools.Execute(ctx, name, input)
		}
	}
	if s.operations != nil {
		if _, ok := s.operations.Get(name); ok {
			return s.operations.Execute(ctx, name, input)
		}
	}
	return nil, fmt.Errorf("unknown tool: %s", name)
}

func (s *Server) registeredOperationNames() []string {
	if s == nil || s.operations == nil {
		return nil
	}
	return s.operations.Names()
}

func (s *Server) allRegisteredNames() []string {
	seen := map[string]struct{}{}
	for _, catalog := range []*registry.Registry{s.tools, s.operations} {
		if catalog == nil {
			continue
		}
		for _, name := range catalog.Names() {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// modelSchemaNames starts from the model catalog and admits a service
// operation only when the caller explicitly requested that exact legacy
// identity. An empty allowlist can therefore never flatten service APIs into a
// model prompt.
func (s *Server) modelSchemaNames(allowedTools []string) []string {
	seen := map[string]struct{}{}
	if s != nil && s.tools != nil {
		for _, name := range s.tools.Names() {
			seen[name] = struct{}{}
		}
	}
	if s != nil && s.operations != nil {
		for _, requested := range allowedTools {
			name := strings.TrimSpace(requested)
			if _, ok := s.operations.Get(name); ok {
				seen[name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

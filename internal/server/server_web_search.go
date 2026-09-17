package server

import "context"

func (s *Server) executeWebSearchTool(ctx context.Context, input map[string]any) (any, error) {
	return s.executeRegisteredTool(ctx, "web_search", input)
}

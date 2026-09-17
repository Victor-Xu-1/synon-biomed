package server

import (
	"sort"
	"strings"
)

// ReportRuntimeComponent records only the bounded component identity. Error
// text is deliberately not retained or exposed by the health endpoint.
func (s *Server) ReportRuntimeComponent(name string, componentErr error) {
	if s == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return
	}
	s.runtimeComponentsMu.Lock()
	defer s.runtimeComponentsMu.Unlock()
	if componentErr == nil {
		delete(s.runtimeComponents, name)
		return
	}
	s.runtimeComponents[name] = struct{}{}
}

func (s *Server) degradedRuntimeComponents() []string {
	if s == nil {
		return nil
	}
	s.runtimeComponentsMu.RLock()
	components := make([]string, 0, len(s.runtimeComponents))
	for name := range s.runtimeComponents {
		components = append(components, name)
	}
	s.runtimeComponentsMu.RUnlock()
	sort.Strings(components)
	return components
}

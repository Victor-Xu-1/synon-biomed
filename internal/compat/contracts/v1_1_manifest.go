package contracts

// V11ContractSummary is the immutable contract baseline recovered from the
// SynonBiomed v1.1 client bundle. It is used to prevent Go-runtime regressions.
type V11ContractSummary struct {
	ServiceMethodCount int
	HTTPRouteCount     int
	EventTypeCount     int
	QueryKeyCount      int
}

// V11Summary reports the complete v1.1 client contract surface.
func V11Summary() V11ContractSummary {
	return V11ContractSummary{
		ServiceMethodCount: len(ServiceMethods),
		HTTPRouteCount:     len(HTTPRoutes),
		EventTypeCount:     len(EventTypes),
		QueryKeyCount:      len(QueryKeys),
	}
}

// V11HasHTTPRoute reports whether a METHOD /path identity is part of the
// recovered public compute API.
func V11HasHTTPRoute(name string) bool {
	for _, route := range HTTPRoutes {
		if route.Name() == name {
			return true
		}
	}
	return false
}

// V11HasServiceMethod reports whether name is part of the recovered v1.1 API.
func V11HasServiceMethod(name string) bool {
	for _, method := range ServiceMethods {
		if method.Method == name {
			return true
		}
	}
	return false
}

// V11HasEventType reports whether name is part of the recovered v1.1 event API.
func V11HasEventType(name string) bool {
	for _, event := range EventTypes {
		if event.Name == name {
			return true
		}
	}
	return false
}

// V11HasQueryKey reports whether name is part of the recovered v1.1 query API.
func V11HasQueryKey(name string) bool {
	for _, key := range QueryKeys {
		if key.Name == name {
			return true
		}
	}
	return false
}

// V11DomainMethodCount reports the number of methods in one RPC namespace.
func V11DomainMethodCount(domain string) int {
	for _, item := range Domains() {
		if item.Name == domain {
			return len(item.Methods)
		}
	}
	return 0
}

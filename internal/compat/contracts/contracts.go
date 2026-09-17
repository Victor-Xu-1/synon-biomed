package contracts

// ServiceMethod describes one client-visible RPC or direct route used by the
// recovered Web UI. Generated data lives in generated_contracts.go.
type ServiceMethod struct {
	Method        string   `json:"method"`
	SourceLine    int      `json:"source_line"`
	Params        string   `json:"params"`
	RPCCalls      []string `json:"rpc_calls,omitempty"`
	RouteLiterals []string `json:"route_literals,omitempty"`
}

// HTTPRoute describes a public backend route registered by the v1.1 runtime
// that is not represented by the recovered client service-method wrapper.
type HTTPRoute struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	SourceFile string `json:"source_file"`
	SourceLine int    `json:"source_line"`
}

func (route HTTPRoute) Name() string {
	return route.Method + " " + route.Path
}

// EventType describes one live-update event the Web UI currently expects from
// the agent runtime websocket channel.
type EventType struct {
	Name       string   `json:"name"`
	SourceLine int      `json:"source_line"`
	Kind       string   `json:"kind"`
	Handler    string   `json:"handler,omitempty"`
	Owner      string   `json:"owner,omitempty"`
	Via        string   `json:"via,omitempty"`
	Pins       []string `json:"pins,omitempty"`
}

// QueryKey describes one client cache key shape. The Go runtime does not use
// these directly, but it must preserve invalidation semantics for compatible UI
// behavior during the migration.
type QueryKey struct {
	Name       string `json:"name"`
	SourceLine int    `json:"source_line"`
	Expression string `json:"expression"`
}

// Domain groups related service methods. It is the primary migration unit for
// replacing the old runtime with maintainable Go packages.
type Domain struct {
	Name    string          `json:"name"`
	Methods []ServiceMethod `json:"methods"`
}

// Domains groups generated service methods by their RPC namespace. Direct
// route-based methods are grouped under "direct".
func Domains() []Domain {
	byName := map[string][]ServiceMethod{}
	order := []string{}
	for _, method := range ServiceMethods {
		name := "direct"
		if len(method.RPCCalls) > 0 {
			name = domainFromRPC(method.RPCCalls[0])
		}
		if _, ok := byName[name]; !ok {
			order = append(order, name)
		}
		byName[name] = append(byName[name], method)
	}
	domains := make([]Domain, 0, len(order))
	for _, name := range order {
		domains = append(domains, Domain{Name: name, Methods: byName[name]})
	}
	return domains
}

func domainFromRPC(rpc string) string {
	for i, ch := range rpc {
		if ch == '.' {
			return rpc[:i]
		}
	}
	return rpc
}

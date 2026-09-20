package server

import (
	"reflect"
	"sort"
	"strings"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

type registeredImplementationRoute struct {
	skill        skills.Skill
	pack         sciencecapability.ExecutionPack
	capabilities map[string]bool
	ambiguous    bool
}

func (route *registeredImplementationRoute) directlyProvides(required []string) bool {
	if route == nil || route.ambiguous {
		return false
	}
	for _, capability := range required {
		if !route.capabilities[strings.ToLower(strings.TrimSpace(capability))] {
			return false
		}
	}
	return true
}

// registeredImplementationRouteCandidates mirrors the active primary/resolver
// authority: a primary pack plus its explicitly declared input resolvers. It
// does not select auxiliary routes or attest execution, inputs, or readiness.
func registeredImplementationRouteCandidates(skillCatalog *skills.Catalog, catalog *sciencecapability.Catalog, required []string) []string {
	routes := registeredImplementationRoutes(skillCatalog, catalog)
	identities := map[string]string{}
	owners := map[string]string{}
	for name, route := range routes {
		if route.ambiguous {
			continue
		}
		ownsRequired := false
		for _, capability := range required {
			ownsRequired = ownsRequired || route.capabilities[strings.ToLower(capability)]
		}
		if !ownsRequired {
			continue
		}
		provided := registeredImplementationRouteCapabilities(route, routes)
		complete := true
		for _, capability := range required {
			complete = complete && provided[strings.ToLower(capability)]
		}
		if !complete {
			continue
		}
		for _, identity := range route.skill.ImplementationIdentities {
			key := askUserImplementationIdentityKey(identity)
			if key == "" {
				continue
			}
			if owner, exists := owners[key]; exists && owner != name {
				// Equal display identities cannot merge distinct execution packs.
				return nil
			}
			owners[key], identities[key] = name, strings.TrimSpace(identity)
		}
	}
	result := make([]string, 0, len(identities))
	for _, identity := range identities {
		result = append(result, identity)
	}
	sort.Strings(result)
	return result
}

func registeredImplementationRoutes(skillCatalog *skills.Catalog, catalog *sciencecapability.Catalog) map[string]*registeredImplementationRoute {
	routes := map[string]*registeredImplementationRoute{}
	if skillCatalog == nil || catalog == nil {
		return routes
	}
	for _, capability := range catalog.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			pack := engine.ExecutionPack
			if pack.Mode != "local" {
				continue
			}
			skill, found := findCatalogSkill(skillCatalog, pack.Skill)
			if !found || len(skill.ImplementationIdentities) == 0 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(skill.Name))
			route := routes[key]
			if route == nil {
				route = &registeredImplementationRoute{skill: skill, pack: pack, capabilities: map[string]bool{}}
				routes[key] = route
			} else if !reflect.DeepEqual(route.pack, pack) {
				route.ambiguous = true
			}
			route.capabilities[strings.ToLower(strings.TrimSpace(capability.ID))] = true
		}
	}
	return routes
}

func registeredResolverRouteCapabilities(route *registeredImplementationRoute, routes map[string]*registeredImplementationRoute) map[string]bool {
	groups := map[string][]sciencecapability.ExecutionEvidenceResolver{}
	for _, parameter := range route.pack.Parameters {
		if parameter.Evidence == "resolved-user-input" && (parameter.Type == "number" || parameter.Type == "integer") {
			group := strings.TrimSpace(parameter.EvidenceGroup)
			if group == "" {
				group = parameter.Name
			}
			groups[group] = nil
		}
	}
	for _, resolver := range route.pack.EvidenceResolvers {
		if _, found := groups[resolver.EvidenceGroup]; found {
			groups[resolver.EvidenceGroup] = append(groups[resolver.EvidenceGroup], resolver)
		}
	}
	provided := map[string]bool{}
	for _, alternatives := range groups {
		var common map[string]bool
		for _, resolver := range alternatives {
			target := routes[strings.ToLower(strings.TrimSpace(resolver.Skill))]
			if target == nil || target.ambiguous || !skillSupportsSelectedImplementation(target.skill, []string{resolver.Implementation}) {
				common = nil
				break
			}
			// Alternatives for one input are mutually exclusive. Only their
			// common direct capabilities can support primary-route discovery.
			// Recursive dependencies are not granted by this selection scope.
			if common == nil {
				common = copyImplementationCapabilities(target.capabilities)
			} else {
				for capability := range common {
					if !target.capabilities[capability] {
						delete(common, capability)
					}
				}
			}
		}
		for capability := range common {
			provided[capability] = true
		}
	}
	return provided
}

func registeredImplementationRouteCapabilities(route *registeredImplementationRoute, routes map[string]*registeredImplementationRoute) map[string]bool {
	provided := copyImplementationCapabilities(route.capabilities)
	for capability := range registeredResolverRouteCapabilities(route, routes) {
		provided[capability] = true
	}
	return provided
}

func copyImplementationCapabilities(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for capability, present := range source {
		result[capability] = present
	}
	return result
}

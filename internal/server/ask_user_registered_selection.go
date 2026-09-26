package server

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/sciencecapability"
)

// Registered labels are protocol identities, not arbitrary prose. A compound
// label can name a primary and its explicitly registered input resolvers, but
// cannot invent a composite implementation or combine independent engines.
func (s *Server) registeredAskUserLabelRoute(label string) (string, []sciencecapability.ExecutionEvidenceResolver, bool) {
	if s == nil || s.skillCatalog == nil || s.scienceCapabilities == nil {
		return "", nil, false
	}
	routes := registeredImplementationRoutes(s.skillCatalog, s.scienceCapabilities)
	selected := map[string]string{}
	for _, component := range strings.Split(label, "+") {
		component = strings.TrimSpace(component)
		match, identity := "", ""
		for name, route := range routes {
			if route.ambiguous {
				continue
			}
			for _, registered := range route.skill.ImplementationIdentities {
				if !registeredAskUserLabelComponent(component, registered) {
					continue
				}
				if match != "" && match != name {
					return "", nil, false
				}
				match, identity = name, strings.TrimSpace(registered)
			}
		}
		if match == "" {
			return "", nil, false
		}
		selected[match] = identity
	}
	primary := ""
	var resolvers []sciencecapability.ExecutionEvidenceResolver
	for name, identity := range selected {
		var dependencies []sciencecapability.ExecutionEvidenceResolver
		covered := map[string]bool{name: true}
		groups := map[string]string{}
		for _, resolver := range routes[name].pack.EvidenceResolvers {
			dependency := strings.ToLower(strings.TrimSpace(resolver.Skill))
			if selectedIdentity, found := selected[dependency]; found && askUserImplementationIdentityMatches(selectedIdentity, resolver.Implementation) {
				if previous := groups[resolver.EvidenceGroup]; previous != "" && previous != dependency {
					return "", nil, false
				}
				groups[resolver.EvidenceGroup], covered[dependency] = dependency, true
				dependencies = append(dependencies, resolver)
			}
		}
		if len(covered) != len(selected) || len(dependencies) > 1 {
			continue
		}
		if primary != "" {
			return "", nil, false
		}
		primary, resolvers = identity, dependencies
	}
	return primary, resolvers, primary != ""
}

func registeredAskUserLabelComponent(label, identity string) bool {
	identity = strings.TrimSpace(identity)
	if identity == "" || len(label) < len(identity) || !strings.EqualFold(label[:len(identity)], identity) {
		return false
	}
	suffix := strings.TrimSpace(label[len(identity):])
	if suffix == "" {
		return true
	}
	// Older localized labels append their capability name directly after an
	// exact Latin implementation identity. Versions, aliases, comparative prose
	// and leading instructions are not recovered by substring matching.
	first, _ := utf8.DecodeRuneInString(suffix)
	if !unicode.Is(unicode.Han, first) {
		return false
	}
	for _, r := range suffix {
		if !unicode.Is(unicode.Han, r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func (s *Server) bindAskUserRegisteredOptionIdentities(questions []askUserQuestion) []askUserQuestion {
	bound := append([]askUserQuestion(nil), questions...)
	for index := range bound {
		bound[index].Options = append([]askUserQuestionOption(nil), bound[index].Options...)
		for optionIndex := range bound[index].Options {
			option := &bound[index].Options[optionIndex]
			if stringValue(option.Metadata["implementation"]) != "" || len(mapValue(option.Metadata["evidence_resolver"])) > 0 {
				continue
			}
			primary, _, found := s.registeredAskUserLabelRoute(option.Label)
			if !found {
				continue
			}
			option.Metadata = copyMapAny(option.Metadata)
			option.Metadata["implementation"] = primary
			if option.Metadata["resources"] == nil {
				option.Metadata["resources"] = map[string]any{"cpu": "unresolved", "memory": "unresolved", "gpu": "unresolved"}
			}
		}
	}
	return bound
}

// Both answer acceptance and restart projection use this same reconciler.
// Only a selected, immutable closed option can repair absent identity fields;
// a free-text answer, unselected option or model narrative cannot grant them.
func (s *Server) reconcileAnsweredAskUserSelection(item map[string]any, answers map[string]string, continuation string) (string, error) {
	var value map[string]any
	if json.Unmarshal([]byte(continuation), &value) != nil || value["status"] != "answered" {
		return continuation, nil
	}
	questions := anySliceValue(item["questions"])
	if stringValue(item["question"]) != "" {
		questions = []any{item}
	}
	implementations := mapValue(value["implementations"])
	if implementations == nil {
		implementations = map[string]any{}
	}
	resolvers := mapValue(value["evidence_resolvers"])
	if resolvers == nil {
		resolvers = map[string]any{}
	}
	changed := false
	for _, rawQuestion := range questions {
		question := mapValue(rawQuestion)
		key := stringValue(question["question"])
		answer, answered := answers[key]
		if !answered {
			continue
		}
		for _, rawOption := range anySliceValue(question["options"]) {
			option := mapValue(rawOption)
			metadata := mapValue(option["metadata"])
			if answer != stringValue(option["label"]) || len(mapValue(metadata["evidence_resolver"])) > 0 {
				continue
			}
			primary, dependencies, found := s.registeredAskUserLabelRoute(answer)
			if !found || (stringValue(metadata["implementation"]) != "" && !askUserImplementationIdentityMatches(stringValue(metadata["implementation"]), primary)) {
				continue
			}
			if existing := stringValue(implementations[key]); existing != "" && !askUserImplementationIdentityMatches(existing, primary) {
				continue
			}
			if existing := mapValue(resolvers[key]); len(existing) > 0 {
				if len(dependencies) != 1 || stringValue(existing["evidence_group"]) != dependencies[0].EvidenceGroup ||
					stringValue(existing["skill"]) != dependencies[0].Skill ||
					!askUserImplementationIdentityMatches(stringValue(existing["implementation"]), dependencies[0].Implementation) {
					continue
				}
			}
			implementations[key], changed = primary, true
			for _, resolver := range dependencies {
				resolvers[key] = transcriptstore.AskUserEvidenceResolverSelection{
					EvidenceGroup: resolver.EvidenceGroup, Skill: resolver.Skill, Implementation: resolver.Implementation,
				}
			}
		}
	}
	if !changed {
		return continuation, nil
	}
	value["implementations"] = implementations
	if len(resolvers) > 0 {
		value["evidence_resolvers"] = resolvers
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

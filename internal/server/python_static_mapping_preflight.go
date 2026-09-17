package server

import (
	"fmt"
	"sort"
	"strings"
)

// This file implements a deliberately small, conservative Python literal
// analyzer for software_runtime source preflight. It recognizes only values
// whose shape can be proven without importing or executing user code. Any
// dynamic expression is ignored and remains under the post-execution quality
// gate.

type pythonStaticToken struct {
	kind string
	text string
	line int
}

type pythonStaticValue struct {
	kind     string
	text     string
	mapping  map[string]pythonStaticValue
	sequence []pythonStaticValue
}

type pythonStaticAssignment struct {
	value pythonStaticValue
	line  int
}

const (
	pythonStaticUnknown  = "unknown"
	pythonStaticString   = "string"
	pythonStaticScalar   = "scalar"
	pythonStaticRef      = "ref"
	pythonStaticMapping  = "mapping"
	pythonStaticSequence = "sequence"
)

func pythonStaticMappingContractDiagnostics(source, sourceLabel string) []any {
	tokens := tokenizePythonStaticSource(source)
	assignments := pythonStaticAssignments(tokens)
	bindings := pythonStaticLoopBindings(tokens)
	valueBindings := pythonStaticValueLoopBindings(tokens)
	diagnostics := make([]any, 0)
	seen := make(map[string]struct{})

	// A literal mapping-of-mappings proves a nested field is impossible when
	// every value omits it, even if the outer selector came from another
	// same-key collection. This catches MAP[name]["field"] without guessing
	// which outer key name holds at runtime.
	for index := 0; index < len(tokens); index++ {
		mappingName, selectorName, field, next, ok := pythonStaticNestedMappingSelector(tokens, index)
		if !ok {
			continue
		}
		index = next - 1
		if pythonStaticNameMutated(tokens, mappingName) {
			continue
		}
		mapping, ok := pythonStaticResolveAssignment(mappingName, assignments, nil)
		if !ok || mapping.kind != pythonStaticMapping || len(mapping.mapping) == 0 {
			continue
		}
		missing := make([]string, 0, len(mapping.mapping))
		availableSet := make(map[string]struct{})
		proven := true
		for label, item := range mapping.mapping {
			if item.kind != pythonStaticMapping {
				proven = false
				break
			}
			for key := range item.mapping {
				availableSet[key] = struct{}{}
			}
			if _, found := item.mapping[field]; !found {
				missing = append(missing, label)
			}
		}
		if !proven || len(missing) != len(mapping.mapping) {
			continue
		}
		sort.Strings(missing)
		dedupKey := "nested\x00" + mappingName + "\x00" + field
		if _, found := seen[dedupKey]; found {
			continue
		}
		seen[dedupKey] = struct{}{}
		diagnostics = append(diagnostics, map[string]any{
			"code": "python_static_indexed_mapping_field_unavailable", "source": sourceLabel,
			"line": tokens[index].line, "mapping": mappingName,
			"selector_variable": selectorName, "required_field": field,
			"missing_items": missing, "available_fields": pythonStaticSortedKeys(availableSet),
			"recovery": "add the required field to every proven nested mapping or replace the access with an existing field before executing the same software plan",
		})
	}

	for index := 0; index < len(tokens); index++ {
		mappingName, selectorName, selectorField, next, ok := pythonStaticMappingSelector(tokens, index)
		if !ok {
			continue
		}
		index = next - 1
		collectionName, bound := bindings[selectorName]
		if !bound || pythonStaticNameMutated(tokens, mappingName) {
			continue
		}
		mapping, ok := pythonStaticResolveAssignment(mappingName, assignments, nil)
		if !ok || mapping.kind != pythonStaticMapping {
			continue
		}
		collection, ok := pythonStaticResolveAssignment(collectionName, assignments, nil)
		if !ok || collection.kind != pythonStaticSequence || len(collection.sequence) == 0 {
			continue
		}

		missingSet := make(map[string]struct{})
		selectorValues := make([]string, 0, len(collection.sequence))
		proven := true
		for _, item := range collection.sequence {
			if item.kind != pythonStaticMapping {
				proven = false
				break
			}
			selected, found := item.mapping[selectorField]
			if !found || selected.kind != pythonStaticString {
				proven = false
				break
			}
			selectorValues = append(selectorValues, selected.text)
			if _, found := mapping.mapping[selected.text]; !found {
				missingSet[selected.text] = struct{}{}
			}
		}
		if !proven || len(missingSet) == 0 {
			continue
		}
		missing := pythonStaticSortedKeys(missingSet)
		available := make([]string, 0, len(mapping.mapping))
		for key := range mapping.mapping {
			available = append(available, key)
		}
		sort.Strings(available)
		sort.Strings(selectorValues)
		dedupKey := mappingName + "\x00" + collectionName + "\x00" + selectorField + "\x00" + strings.Join(missing, "\x00")
		if _, found := seen[dedupKey]; found {
			continue
		}
		seen[dedupKey] = struct{}{}
		diagnostics = append(diagnostics, map[string]any{
			"code": "python_static_mapping_key_unavailable", "source": sourceLabel,
			"line": tokens[index].line, "mapping": mappingName,
			"iterator": collectionName, "selector_field": selectorField,
			"selector_values": selectorValues, "missing_keys": missing,
			"available_keys": available,
			"recovery":       "align the static mapping keys with every proven selector value before executing the same software plan",
		})
	}
	for index := 0; index < len(tokens); index++ {
		iteratorName, field, next, ok := pythonStaticDirectMappingSelector(tokens, index)
		if !ok {
			continue
		}
		index = next - 1
		binding, bound := valueBindings[iteratorName]
		if !bound || pythonStaticNameMutated(tokens, iteratorName) {
			continue
		}
		collection, ok := pythonStaticResolveAssignment(binding.collection, assignments, nil)
		if !ok {
			continue
		}
		items, labels, ok := pythonStaticBoundMappingItems(collection, binding.mappingValues)
		if !ok || len(items) == 0 {
			continue
		}
		missing := make([]string, 0)
		availableSet := make(map[string]struct{})
		proven := true
		for position, item := range items {
			if item.kind != pythonStaticMapping {
				proven = false
				break
			}
			for key := range item.mapping {
				availableSet[key] = struct{}{}
			}
			if _, found := item.mapping[field]; !found {
				missing = append(missing, labels[position])
			}
		}
		if !proven || len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		dedupKey := iteratorName + "\x00" + binding.collection + "\x00" + field + "\x00" + strings.Join(missing, "\x00")
		if _, found := seen[dedupKey]; found {
			continue
		}
		seen[dedupKey] = struct{}{}
		diagnostics = append(diagnostics, map[string]any{
			"code": "python_static_iterated_mapping_field_unavailable", "source": sourceLabel,
			"line": tokens[index].line, "iterator_variable": iteratorName,
			"iterator": binding.collection, "required_field": field,
			"missing_items": missing, "available_fields": pythonStaticSortedKeys(availableSet),
			"recovery": "add the required field to every proven iterated mapping or use a guarded/default lookup before executing the same software plan",
		})
	}
	return diagnostics
}

type pythonStaticValueLoopBinding struct {
	collection    string
	mappingValues bool
}

// pythonStaticValueLoopBindings recognizes only direct iteration over a
// literal sequence and the value variable of `for key, value in MAP.items()`.
// More dynamic iterators deliberately remain a runtime concern.
func pythonStaticValueLoopBindings(tokens []pythonStaticToken) map[string]pythonStaticValueLoopBinding {
	result := make(map[string]pythonStaticValueLoopBinding)
	conflicts := make(map[string]struct{})
	add := func(variable string, binding pythonStaticValueLoopBinding) {
		if variable == "" || binding.collection == "" {
			return
		}
		if existing, found := result[variable]; found && existing != binding {
			delete(result, variable)
			conflicts[variable] = struct{}{}
			return
		}
		if _, conflict := conflicts[variable]; !conflict {
			result[variable] = binding
		}
	}
	for index := 0; index < len(tokens); index++ {
		if tokens[index].kind != "identifier" || tokens[index].text != "for" {
			continue
		}
		first := pythonStaticNext(tokens, index+1)
		if first >= len(tokens) || tokens[first].kind != "identifier" {
			continue
		}
		next := pythonStaticNext(tokens, first+1)
		if next < len(tokens) && tokens[next].kind == "identifier" && tokens[next].text == "in" {
			collection := pythonStaticNext(tokens, next+1)
			if collection < len(tokens) && tokens[collection].kind == "identifier" {
				add(tokens[first].text, pythonStaticValueLoopBinding{collection: tokens[collection].text})
			}
			continue
		}
		if next >= len(tokens) || tokens[next].text != "," {
			continue
		}
		valueVariable := pythonStaticNext(tokens, next+1)
		in := pythonStaticNext(tokens, valueVariable+1)
		collection := pythonStaticNext(tokens, in+1)
		dot := pythonStaticNext(tokens, collection+1)
		method := pythonStaticNext(tokens, dot+1)
		open := pythonStaticNext(tokens, method+1)
		close := pythonStaticNext(tokens, open+1)
		if close >= len(tokens) || tokens[valueVariable].kind != "identifier" ||
			tokens[in].kind != "identifier" || tokens[in].text != "in" ||
			tokens[collection].kind != "identifier" || tokens[dot].text != "." ||
			tokens[method].kind != "identifier" || tokens[method].text != "items" ||
			tokens[open].text != "(" || tokens[close].text != ")" {
			continue
		}
		add(tokens[valueVariable].text, pythonStaticValueLoopBinding{
			collection: tokens[collection].text, mappingValues: true,
		})
	}
	return result
}

func pythonStaticDirectMappingSelector(tokens []pythonStaticToken, index int) (string, string, int, bool) {
	if index >= len(tokens) || tokens[index].kind != "identifier" {
		return "", "", index, false
	}
	open := pythonStaticNext(tokens, index+1)
	field := pythonStaticNext(tokens, open+1)
	close := pythonStaticNext(tokens, field+1)
	if close >= len(tokens) || tokens[open].text != "[" ||
		tokens[field].kind != pythonStaticString || tokens[close].text != "]" {
		return "", "", index, false
	}
	after := pythonStaticNext(tokens, close+1)
	if after < len(tokens) && tokens[after].text == "=" {
		return "", "", index, false
	}
	return tokens[index].text, tokens[field].text, close + 1, true
}

func pythonStaticBoundMappingItems(
	collection pythonStaticValue,
	mappingValues bool,
) ([]pythonStaticValue, []string, bool) {
	if !mappingValues {
		if collection.kind != pythonStaticSequence {
			return nil, nil, false
		}
		labels := make([]string, len(collection.sequence))
		for index := range labels {
			labels[index] = fmt.Sprintf("index:%d", index)
		}
		return collection.sequence, labels, true
	}
	if collection.kind != pythonStaticMapping {
		return nil, nil, false
	}
	labels := make([]string, 0, len(collection.mapping))
	for key := range collection.mapping {
		labels = append(labels, key)
	}
	sort.Strings(labels)
	items := make([]pythonStaticValue, 0, len(labels))
	for _, label := range labels {
		items = append(items, collection.mapping[label])
	}
	return items, labels, true
}

func tokenizePythonStaticSource(source string) []pythonStaticToken {
	tokens := make([]pythonStaticToken, 0, len(source)/4)
	line := 1
	for index := 0; index < len(source); {
		current := source[index]
		switch {
		case current == '\n':
			tokens = append(tokens, pythonStaticToken{kind: "newline", text: "\n", line: line})
			line++
			index++
		case current == '\r' || current == ' ' || current == '\t' || current == '\f':
			index++
		case current == '#':
			for index < len(source) && source[index] != '\n' {
				index++
			}
		case current == '\'' || current == '"':
			value, next, literal := scanPythonStaticString(source, index)
			kind := "opaque"
			if literal {
				kind = pythonStaticString
			}
			tokens = append(tokens, pythonStaticToken{kind: kind, text: value, line: line})
			line += strings.Count(source[index:next], "\n")
			index = next
		case pythonStaticIdentifierStart(current):
			start := index
			index++
			for index < len(source) && pythonStaticIdentifierContinue(source[index]) {
				index++
			}
			tokens = append(tokens, pythonStaticToken{kind: "identifier", text: source[start:index], line: line})
		case current >= '0' && current <= '9':
			start := index
			index++
			for index < len(source) {
				value := source[index]
				if (value >= '0' && value <= '9') || value == '.' || value == '_' ||
					value == 'e' || value == 'E' || value == '+' || value == '-' ||
					(value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F') || value == 'x' || value == 'X' {
					index++
					continue
				}
				break
			}
			tokens = append(tokens, pythonStaticToken{kind: "number", text: source[start:index], line: line})
		default:
			tokens = append(tokens, pythonStaticToken{kind: "punct", text: string(current), line: line})
			index++
		}
	}
	return tokens
}

func scanPythonStaticString(source string, start int) (string, int, bool) {
	quote := source[start]
	triple := start+2 < len(source) && source[start+1] == quote && source[start+2] == quote
	index := start + 1
	if triple {
		index = start + 3
	}
	var value strings.Builder
	literal := !triple
	for index < len(source) {
		if triple && index+2 < len(source) && source[index] == quote && source[index+1] == quote && source[index+2] == quote {
			return value.String(), index + 3, false
		}
		if !triple && source[index] == quote {
			return value.String(), index + 1, literal
		}
		if !triple && source[index] == '\n' {
			return value.String(), index, false
		}
		if source[index] == '\\' {
			literal = false
			if index+1 < len(source) {
				index += 2
				continue
			}
		}
		value.WriteByte(source[index])
		index++
	}
	return value.String(), index, false
}

func pythonStaticIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func pythonStaticIdentifierContinue(value byte) bool {
	return pythonStaticIdentifierStart(value) || value >= '0' && value <= '9'
}

func pythonStaticAssignments(tokens []pythonStaticToken) map[string]pythonStaticAssignment {
	result := make(map[string]pythonStaticAssignment)
	duplicates := make(map[string]struct{})
	for index := 0; index+2 < len(tokens); index++ {
		if tokens[index].kind != "identifier" || !pythonStaticStatementStart(tokens, index) {
			continue
		}
		equals := pythonStaticNext(tokens, index+1)
		if equals >= len(tokens) || tokens[equals].text != "=" {
			continue
		}
		value, _, ok := parsePythonStaticValue(tokens, equals+1)
		if !ok {
			continue
		}
		name := tokens[index].text
		if _, found := result[name]; found {
			delete(result, name)
			duplicates[name] = struct{}{}
			continue
		}
		if _, duplicate := duplicates[name]; duplicate {
			continue
		}
		result[name] = pythonStaticAssignment{value: value, line: tokens[index].line}
	}
	return result
}

func pythonStaticStatementStart(tokens []pythonStaticToken, index int) bool {
	if index == 0 {
		return true
	}
	return tokens[index-1].kind == "newline" || tokens[index-1].text == ";"
}

func parsePythonStaticValue(tokens []pythonStaticToken, index int) (pythonStaticValue, int, bool) {
	index = pythonStaticNext(tokens, index)
	if index >= len(tokens) {
		return pythonStaticValue{}, index, false
	}
	token := tokens[index]
	switch token.kind {
	case pythonStaticString:
		return pythonStaticValue{kind: pythonStaticString, text: token.text}, index + 1, true
	case "number":
		return pythonStaticValue{kind: pythonStaticScalar, text: token.text}, index + 1, true
	case "identifier":
		if token.text == "dict" {
			open := pythonStaticNext(tokens, index+1)
			if open < len(tokens) && tokens[open].text == "(" {
				return parsePythonStaticDictCall(tokens, open)
			}
		}
		if token.text == "True" || token.text == "False" || token.text == "None" {
			return pythonStaticValue{kind: pythonStaticScalar, text: token.text}, index + 1, true
		}
		return pythonStaticValue{kind: pythonStaticRef, text: token.text}, index + 1, true
	case "punct":
		switch token.text {
		case "{":
			return parsePythonStaticMapping(tokens, index)
		case "[":
			return parsePythonStaticSequence(tokens, index, "]")
		case "(":
			return parsePythonStaticSequence(tokens, index, ")")
		case "-", "+":
			value, next, ok := parsePythonStaticValue(tokens, index+1)
			if ok && value.kind == pythonStaticScalar {
				value.text = token.text + value.text
				return value, next, true
			}
		}
	}
	return pythonStaticValue{kind: pythonStaticUnknown}, index + 1, false
}

func parsePythonStaticMapping(tokens []pythonStaticToken, open int) (pythonStaticValue, int, bool) {
	result := make(map[string]pythonStaticValue)
	index := open + 1
	for {
		index = pythonStaticNext(tokens, index)
		if index >= len(tokens) {
			return pythonStaticValue{}, index, false
		}
		if tokens[index].text == "}" {
			return pythonStaticValue{kind: pythonStaticMapping, mapping: result}, index + 1, true
		}
		if tokens[index].kind != pythonStaticString {
			return pythonStaticValue{}, index, false
		}
		key := tokens[index].text
		colon := pythonStaticNext(tokens, index+1)
		if colon >= len(tokens) || tokens[colon].text != ":" {
			return pythonStaticValue{}, colon, false
		}
		value, next, ok := parsePythonStaticValue(tokens, colon+1)
		if !ok {
			return pythonStaticValue{}, next, false
		}
		result[key] = value
		index = pythonStaticNext(tokens, next)
		if index < len(tokens) && tokens[index].text == "," {
			index++
			continue
		}
		if index < len(tokens) && tokens[index].text == "}" {
			return pythonStaticValue{kind: pythonStaticMapping, mapping: result}, index + 1, true
		}
		return pythonStaticValue{}, index, false
	}
}

func parsePythonStaticDictCall(tokens []pythonStaticToken, open int) (pythonStaticValue, int, bool) {
	result := make(map[string]pythonStaticValue)
	index := open + 1
	for {
		index = pythonStaticNext(tokens, index)
		if index >= len(tokens) {
			return pythonStaticValue{}, index, false
		}
		if tokens[index].text == ")" {
			return pythonStaticValue{kind: pythonStaticMapping, mapping: result}, index + 1, true
		}
		if tokens[index].kind != "identifier" {
			return pythonStaticValue{}, index, false
		}
		key := tokens[index].text
		equals := pythonStaticNext(tokens, index+1)
		if equals >= len(tokens) || tokens[equals].text != "=" {
			return pythonStaticValue{}, equals, false
		}
		value, next, ok := parsePythonStaticValue(tokens, equals+1)
		if !ok {
			return pythonStaticValue{}, next, false
		}
		result[key] = value
		index = pythonStaticNext(tokens, next)
		if index < len(tokens) && tokens[index].text == "," {
			index++
			continue
		}
		if index < len(tokens) && tokens[index].text == ")" {
			return pythonStaticValue{kind: pythonStaticMapping, mapping: result}, index + 1, true
		}
		return pythonStaticValue{}, index, false
	}
}

func parsePythonStaticSequence(tokens []pythonStaticToken, open int, close string) (pythonStaticValue, int, bool) {
	result := make([]pythonStaticValue, 0)
	index := open + 1
	for {
		index = pythonStaticNext(tokens, index)
		if index >= len(tokens) {
			return pythonStaticValue{}, index, false
		}
		if tokens[index].text == close {
			return pythonStaticValue{kind: pythonStaticSequence, sequence: result}, index + 1, true
		}
		value, next, ok := parsePythonStaticValue(tokens, index)
		if !ok {
			return pythonStaticValue{}, next, false
		}
		result = append(result, value)
		index = pythonStaticNext(tokens, next)
		if index < len(tokens) && tokens[index].text == "," {
			index++
			continue
		}
		if index < len(tokens) && tokens[index].text == close {
			return pythonStaticValue{kind: pythonStaticSequence, sequence: result}, index + 1, true
		}
		return pythonStaticValue{}, index, false
	}
}

func pythonStaticNext(tokens []pythonStaticToken, index int) int {
	for index < len(tokens) && tokens[index].kind == "newline" {
		index++
	}
	return index
}

func pythonStaticResolveAssignment(
	name string,
	assignments map[string]pythonStaticAssignment,
	seen map[string]struct{},
) (pythonStaticValue, bool) {
	assignment, found := assignments[name]
	if !found {
		return pythonStaticValue{}, false
	}
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if _, cycle := seen[name]; cycle {
		return pythonStaticValue{}, false
	}
	nextSeen := make(map[string]struct{}, len(seen)+1)
	for key := range seen {
		nextSeen[key] = struct{}{}
	}
	nextSeen[name] = struct{}{}
	return pythonStaticResolveValue(assignment.value, assignments, nextSeen)
}

func pythonStaticResolveValue(
	value pythonStaticValue,
	assignments map[string]pythonStaticAssignment,
	seen map[string]struct{},
) (pythonStaticValue, bool) {
	switch value.kind {
	case pythonStaticRef:
		return pythonStaticResolveAssignment(value.text, assignments, seen)
	case pythonStaticMapping:
		resolved := make(map[string]pythonStaticValue, len(value.mapping))
		for key, item := range value.mapping {
			current, ok := pythonStaticResolveValue(item, assignments, seen)
			if !ok {
				current = pythonStaticValue{kind: pythonStaticUnknown}
			}
			resolved[key] = current
		}
		value.mapping = resolved
		return value, true
	case pythonStaticSequence:
		resolved := make([]pythonStaticValue, 0, len(value.sequence))
		for _, item := range value.sequence {
			current, ok := pythonStaticResolveValue(item, assignments, seen)
			if !ok {
				return pythonStaticValue{}, false
			}
			resolved = append(resolved, current)
		}
		value.sequence = resolved
		return value, true
	case pythonStaticString, pythonStaticScalar:
		return value, true
	default:
		return pythonStaticValue{}, false
	}
}

func pythonStaticLoopBindings(tokens []pythonStaticToken) map[string]string {
	result := make(map[string]string)
	conflicts := make(map[string]struct{})
	for index := 0; index < len(tokens); index++ {
		if tokens[index].kind != "identifier" || tokens[index].text != "for" {
			continue
		}
		variable := pythonStaticNext(tokens, index+1)
		in := pythonStaticNext(tokens, variable+1)
		collection := pythonStaticNext(tokens, in+1)
		if collection >= len(tokens) || tokens[variable].kind != "identifier" ||
			tokens[in].kind != "identifier" || tokens[in].text != "in" ||
			tokens[collection].kind != "identifier" {
			continue
		}
		name, source := tokens[variable].text, tokens[collection].text
		if existing, found := result[name]; found && existing != source {
			delete(result, name)
			conflicts[name] = struct{}{}
			continue
		}
		if _, conflict := conflicts[name]; !conflict {
			result[name] = source
		}
	}
	return result
}

func pythonStaticMappingSelector(tokens []pythonStaticToken, index int) (string, string, string, int, bool) {
	if index >= len(tokens) || tokens[index].kind != "identifier" {
		return "", "", "", index, false
	}
	openOuter := pythonStaticNext(tokens, index+1)
	selector := pythonStaticNext(tokens, openOuter+1)
	openInner := pythonStaticNext(tokens, selector+1)
	field := pythonStaticNext(tokens, openInner+1)
	closeInner := pythonStaticNext(tokens, field+1)
	closeOuter := pythonStaticNext(tokens, closeInner+1)
	if closeOuter >= len(tokens) || tokens[openOuter].text != "[" || tokens[selector].kind != "identifier" ||
		tokens[openInner].text != "[" || tokens[field].kind != pythonStaticString ||
		tokens[closeInner].text != "]" || tokens[closeOuter].text != "]" {
		return "", "", "", index, false
	}
	return tokens[index].text, tokens[selector].text, tokens[field].text, closeOuter + 1, true
}

func pythonStaticNestedMappingSelector(tokens []pythonStaticToken, index int) (string, string, string, int, bool) {
	if index >= len(tokens) || tokens[index].kind != "identifier" {
		return "", "", "", index, false
	}
	openOuter := pythonStaticNext(tokens, index+1)
	selector := pythonStaticNext(tokens, openOuter+1)
	closeOuter := pythonStaticNext(tokens, selector+1)
	openInner := pythonStaticNext(tokens, closeOuter+1)
	field := pythonStaticNext(tokens, openInner+1)
	closeInner := pythonStaticNext(tokens, field+1)
	if closeInner >= len(tokens) || tokens[openOuter].text != "[" ||
		tokens[selector].kind != "identifier" || tokens[closeOuter].text != "]" ||
		tokens[openInner].text != "[" || tokens[field].kind != pythonStaticString ||
		tokens[closeInner].text != "]" {
		return "", "", "", index, false
	}
	return tokens[index].text, tokens[selector].text, tokens[field].text, closeInner + 1, true
}

func pythonStaticNameMutated(tokens []pythonStaticToken, name string) bool {
	mutatingMethods := map[string]struct{}{
		"clear": {}, "pop": {}, "popitem": {}, "setdefault": {}, "update": {}, "__setitem__": {},
	}
	for index := 0; index < len(tokens); index++ {
		if tokens[index].kind != "identifier" || tokens[index].text != name {
			continue
		}
		next := pythonStaticNext(tokens, index+1)
		if next >= len(tokens) {
			continue
		}
		if tokens[next].text == "." {
			method := pythonStaticNext(tokens, next+1)
			open := pythonStaticNext(tokens, method+1)
			if open < len(tokens) {
				_, mutates := mutatingMethods[tokens[method].text]
				if mutates && tokens[open].text == "(" {
					return true
				}
			}
		}
		if tokens[next].text != "[" {
			continue
		}
		depth := 0
		for cursor := next; cursor < len(tokens); cursor++ {
			switch tokens[cursor].text {
			case "[":
				depth++
			case "]":
				depth--
				if depth == 0 {
					after := pythonStaticNext(tokens, cursor+1)
					if after < len(tokens) && tokens[after].text == "=" {
						return true
					}
					cursor = len(tokens)
				}
			}
		}
	}
	return false
}

func pythonStaticSortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

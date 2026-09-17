package realtime

import (
	"reflect"
	"testing"

	"synon-go/internal/compat/contracts"
)

// Published catalogs must cover the executable contracts without depending on
// historical engineering reports, which are not source or runtime inputs.
func TestRealtimeCatalogExportsEveryRegisteredContract(t *testing.T) {
	events, queries := EventSpecs(), QueryContracts()
	if len(events) != 47 || len(queries) != 60 || len(events)+len(queries) != 107 {
		t.Fatalf("catalog counts events=%d queries=%d", len(events), len(queries))
	}
	if err := ValidateCatalog(); err != nil {
		t.Fatal(err)
	}
	eventKinds := make(map[string]DeliveryKind, len(events))
	for _, event := range events {
		if event.Name == "" || event.Kind == "" {
			t.Fatalf("incomplete event specification: %#v", event)
		}
		if _, duplicate := eventKinds[event.Name]; duplicate {
			t.Fatalf("duplicate exported event %q", event.Name)
		}
		resolved, found := LookupEvent(event.Name)
		if !found || resolved != event {
			t.Fatalf("exported event %q differs from lookup: %#v", event.Name, resolved)
		}
		switch event.Kind {
		case DeliveryFanout:
			if event.Owner == "" {
				t.Fatalf("fanout event %q has no owner", event.Name)
			}
		case DeliveryNone:
			if event.Reason == "" {
				t.Fatalf("unrouted event %q has no explicit reason", event.Name)
			}
		}
		eventKinds[event.Name] = event.Kind
	}
	for _, expected := range contracts.EventTypes {
		if string(eventKinds[expected.Name]) != expected.Kind {
			t.Fatalf("event %q kind=%q want=%q", expected.Name, eventKinds[expected.Name], expected.Kind)
		}
	}
	queryExpressions := make(map[string]string, len(queries))
	for _, query := range queries {
		if query.Name == "" || query.Expression == "" {
			t.Fatalf("incomplete query specification: %#v", query)
		}
		if _, duplicate := queryExpressions[query.Name]; duplicate {
			t.Fatalf("duplicate exported query %q", query.Name)
		}
		queryExpressions[query.Name] = query.Expression
	}
	for _, expected := range contracts.QueryKeys {
		if queryExpressions[expected.Name] != expected.Expression {
			t.Fatalf("query %q expression=%q want=%q", expected.Name, queryExpressions[expected.Name], expected.Expression)
		}
	}
	if len(eventKinds) != len(contracts.EventTypes) || len(queryExpressions) != len(contracts.QueryKeys) {
		t.Fatal("exported catalogs differ from the complete registered contract set")
	}
}

func TestRealtimeCatalogExportsDoNotMutateAuthority(t *testing.T) {
	events, queries := EventSpecs(), QueryContracts()
	wantEvents := append([]EventSpec(nil), events...)
	wantQueries := append([]contracts.QueryKey(nil), queries...)
	// Restore even a broken aliasing implementation so a failed assertion cannot
	// contaminate later tests of the shared compatibility contract.
	originalQuery := contracts.QueryKeys[0]
	t.Cleanup(func() { contracts.QueryKeys[0] = originalQuery })
	events[0].Name = "changed-by-consumer"
	queries[0].Name = "changed-by-consumer"
	queries[0].Expression = "changed-by-consumer"
	if !reflect.DeepEqual(EventSpecs(), wantEvents) || !reflect.DeepEqual(QueryContracts(), wantQueries) {
		t.Fatal("consumer mutation changed the authoritative realtime catalogs")
	}
}

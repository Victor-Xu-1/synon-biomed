package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/tools/rcsbsearch"
)

const (
	agentRCSBSearchLimit   = int64(2 << 20)
	agentRCSBSearchTimeout = 45 * time.Second
)

var errAgentRCSBSearchAuthority = errors.New("rcsb search authority is unavailable")

type RCSBStructureSearcher interface {
	Search(context.Context, rcsbsearch.Input) (rcsbsearch.Result, error)
}

func defaultRCSBStructureSearcher(configured RCSBStructureSearcher) RCSBStructureSearcher {
	if configured != nil {
		return configured
	}
	return rcsbsearch.New(rcsbsearch.Options{
		MaxBytes: agentRCSBSearchLimit, Timeout: agentRCSBSearchTimeout,
	})
}

func agentRCSBSearchToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name:        "search_rcsb_structures",
		Description: "Search the official RCSB Search API, hydrate each hit from the official RCSB Data API, and return bounded structure metadata with release dates and source URLs. Results report total_count, retrieved_count, has_more, and next_start; continue with start=next_start when broad structure coverage is required. Use before download_rcsb_file whenever a request depends on latest/newest status, organism, experimental method, complex composition, or resolution. The host builds the validated RCSB query; do not handcraft Search API JSON or guess a PDB ID.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"query": map[string]any{
					"type": "string", "description": "Target, molecule, interaction, or structure terms to search.",
				},
				"term_mode": map[string]any{
					"type": "string", "enum": []string{"all", "any", "phrase"},
					"description": "How query terms are combined. Use all unless an exact phrase or broader recall is intended.",
				},
				"organism": map[string]any{
					"type": "string", "description": "Optional exact NCBI scientific name, for example Homo sapiens.",
				},
				"experimental_method": map[string]any{
					"type": "string", "description": "Optional exact RCSB experimental method, for example X-RAY DIFFRACTION.",
				},
				"minimum_polymer_entity_count": map[string]any{
					"type": "integer", "minimum": 0, "maximum": 100,
					"description": "Optional minimum polymer entity count; use 2 when a protein-protein or protein-peptide complex is required.",
				},
				"minimum_nonpolymer_entity_count": map[string]any{
					"type": "integer", "minimum": 0, "maximum": 100,
					"description": "Optional minimum non-polymer entity count; use 1 when a bound small molecule is required.",
				},
				"maximum_resolution": map[string]any{
					"type": "number", "exclusiveMinimum": 0, "maximum": 100,
					"description": "Optional maximum experimental resolution in angstrom.",
				},
				"sort_by": map[string]any{
					"type": "string", "enum": []string{"release_date_desc", "resolution_asc", "relevance"},
					"description": "Use release_date_desc when the user asks for the latest structure.",
				},
				"limit": map[string]any{
					"type": "integer", "minimum": 1, "maximum": 25,
				},
				"start": map[string]any{
					"type": "integer", "minimum": 0,
					"description": "Pagination offset. Use the previous result's next_start while has_more is true.",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (s *Server) executeAgentRCSBSearch(
	ctx context.Context,
	identity *agentKernelContext,
	input map[string]any,
) (map[string]any, error) {
	if s == nil || s.rcsbSearch == nil || s.transcriptStore == nil || identity == nil {
		return nil, errAgentRCSBSearchAuthority
	}
	allowedFields := map[string]bool{
		"query": true, "term_mode": true, "organism": true, "experimental_method": true,
		"minimum_polymer_entity_count": true, "minimum_nonpolymer_entity_count": true,
		"maximum_resolution": true, "sort_by": true, "limit": true, "start": true,
	}
	for field := range input {
		if !allowedFields[field] {
			return nil, errors.New("rcsb search input contains unsupported fields")
		}
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		return nil, errAgentRCSBSearchAuthority
	}
	run, ok := transcriptArtifactRunFromContext(ctx)
	if !ok || run.Authority == nil || run.SourceEventID <= 0 {
		return nil, errAgentRCSBSearchAuthority
	}
	stream, claim := run.Authority.Stream, run.Authority.Claim
	if stream.OwnerID != access.UserID || stream.ProjectID != access.Frame.ProjectID ||
		stream.RootFrameID != access.Frame.RootFrameID || stream.FrameID != access.Frame.ID {
		return nil, errAgentRCSBSearchAuthority
	}
	if err := s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		validated, validateErr := tx.ValidateLiveRunnerClaim(ctx, claim)
		if validateErr != nil {
			return validateErr
		}
		if validated.UID != stream.UID || validated.OwnerID != stream.OwnerID || validated.FrameID != stream.FrameID {
			return errAgentRCSBSearchAuthority
		}
		return nil
	}); err != nil {
		return nil, errAgentRCSBSearchAuthority
	}
	request, err := parseAgentRCSBSearchInput(input)
	if err != nil {
		return nil, err
	}
	result, err := s.rcsbSearch.Search(ctx, request)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, errors.New("rcsb search result encoding failed")
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil, errors.New("rcsb search result encoding failed")
	}
	return output, nil
}

func parseAgentRCSBSearchInput(input map[string]any) (rcsbsearch.Input, error) {
	limit, err := agentRCSBSearchInteger(input["limit"])
	if err != nil {
		return rcsbsearch.Input{}, err
	}
	start, err := agentRCSBSearchInteger(input["start"])
	if err != nil {
		return rcsbsearch.Input{}, err
	}
	minimumPolymer, err := agentRCSBSearchInteger(input["minimum_polymer_entity_count"])
	if err != nil {
		return rcsbsearch.Input{}, err
	}
	minimumNonpolymer, err := agentRCSBSearchInteger(input["minimum_nonpolymer_entity_count"])
	if err != nil {
		return rcsbsearch.Input{}, err
	}
	maximumResolution, err := agentRCSBSearchNumber(input["maximum_resolution"])
	if err != nil {
		return rcsbsearch.Input{}, err
	}
	return rcsbsearch.Input{
		Query:                        strings.TrimSpace(stringValue(input["query"])),
		TermMode:                     rcsbsearch.TermMode(strings.TrimSpace(stringValue(input["term_mode"]))),
		Organism:                     strings.TrimSpace(stringValue(input["organism"])),
		ExperimentalMethod:           strings.TrimSpace(stringValue(input["experimental_method"])),
		MinimumPolymerEntityCount:    minimumPolymer,
		MinimumNonpolymerEntityCount: minimumNonpolymer,
		MaximumResolution:            maximumResolution,
		SortBy:                       rcsbsearch.SortBy(strings.TrimSpace(stringValue(input["sort_by"]))),
		Limit:                        limit,
		Start:                        start,
	}, nil
}

func agentRCSBSearchInteger(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	switch typed := value.(type) {
	case int:
		return typed, nil
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), nil
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), nil
		}
	}
	return 0, errors.New("rcsb search integer field is invalid")
}

func agentRCSBSearchNumber(value any) (float64, error) {
	if value == nil {
		return 0, nil
	}
	switch typed := value.(type) {
	case int:
		return float64(typed), nil
	case float64:
		return typed, nil
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil {
			return parsed, nil
		}
	}
	return 0, errors.New("rcsb search numeric field is invalid")
}

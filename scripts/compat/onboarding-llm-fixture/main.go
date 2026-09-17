package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`"version_id"\s*:\s*"([^"\\]+)"`)

type chatRequest struct {
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func main() {
	address := strings.TrimSpace(os.Getenv("SYNON_ONBOARDING_FIXTURE_ADDRESS"))
	if address == "" {
		address = "127.0.0.1:39091"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/v1/chat/completions", handleChat)
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("onboarding_fixture_ready address=%s", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"data":   []any{map[string]any{"id": "onboarding-fixture", "object": "model"}},
	})
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	var request chatRequest
	if err := decoder.Decode(&request); err != nil || len(request.Messages) == 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	toolResults := 0
	allText := strings.Builder{}
	for _, message := range request.Messages {
		if message.Role == "tool" {
			toolResults++
		}
		allText.WriteString(message.Content)
		allText.WriteByte('\n')
	}
	switch {
	case toolResults == 0:
		match := versionPattern.FindStringSubmatch(allText.String())
		if len(match) != 2 {
			http.Error(w, "attached version unavailable", http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, completionWithTool("fixture-read", "read_onboarding_attachment", map[string]any{
			"version_id": match[1],
		}))
	case toolResults == 1:
		if !strings.Contains(allText.String(), "sample,outcome") || !strings.Contains(allText.String(), "Translational genomics") {
			http.Error(w, "attachment content unavailable", http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, completionWithTool("fixture-suggest", "ask_user", map[string]any{
			"questions": []any{map[string]any{
				"question": "Where should we start?", "header": "First task",
				"options": []any{
					decisionOption(
						"Audit cohort outcome encoding", "Validate outcome coding and missingness before modeling.",
						"Reduces the risk of invalid downstream comparisons.", "Does not yet produce the full cohort report.",
						"Ready from the complete onboarding attachment.", "Requires only the uploaded cohort data.",
						"An auditable outcome-coding and missingness assessment.",
						"Recommended because outcome validity is prerequisite to the other routes.", true,
					),
					decisionOption(
						"Build a reproducible cohort summary", "Create tables and plots describing the uploaded cohort.",
						"Produces a reviewable baseline for the study team.", "Assumes outcome coding is already reliable.",
						"Provisionable from the complete onboarding attachment.", "Requires the uploaded cohort data and local analytical compute.",
						"Editable summary tables, plots, and a concise interpretation.",
						"Choose when the cohort encoding has already been validated.", false,
					),
					decisionOption(
						"Automate cohort quality control", "Turn the recurring checks into a reusable pipeline.",
						"Improves repeatability for future cohorts.", "Automation criteria depend on validated checks.",
						"Provisionable after the initial checks are confirmed.", "Requires representative cohort files and agreed QC criteria.",
						"A reusable cohort quality-control workflow with documented outputs.",
						"Choose when operational reuse is the immediate priority.", false,
					),
				},
			}},
		}))
	default:
		writeJSON(w, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "The selected onboarding task has been recorded.",
			}}},
		})
	}
}

func decisionOption(label, description, pros, cons, readiness, requirements, expectedOutcome, rationale string, recommended bool) map[string]any {
	return map[string]any{
		"label": label, "description": description, "pros": pros, "cons": cons,
		"readiness": readiness, "readiness_status": "unverified",
		"decision_evidence": []any{"tool-call:fixture-read"}, "readiness_evidence": []any{},
		"selection_basis": "scientific_evidence",
		"requirements":    requirements, "expected_outcome": expectedOutcome,
		"selection_rationale": rationale, "recommended": recommended,
	}
}

func completionWithTool(id, name string, arguments map[string]any) map[string]any {
	rawArguments, _ := json.Marshal(arguments)
	return map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": string(rawArguments)},
			}},
		}}},
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

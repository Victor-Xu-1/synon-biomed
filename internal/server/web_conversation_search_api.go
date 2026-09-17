package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	defaultWebConversationSearchPageSize = 20
	maxWebConversationSearchPageSize     = 50
	maxWebConversationSearchQueryRunes   = 200
	maxWebConversationSearchFrames       = 1000
	maxWebConversationSearchMessages     = 2000
)

type webConversationSearchEntry struct {
	Summary     workspace.CompatibilityFrameSummary
	MessageID   string
	MessageType string
	MessageAt   int64
	Preview     string
	MatchKind   string
	MatchCount  int
	Relevance   int
	ModifiedAt  int64
}

func (s *Server) handleWebConversationSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > maxWebConversationSearchQueryRunes {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "search query exceeds 200 characters"})
		return
	}
	page, ok := webConversationSearchInt(r.URL.Query().Get("page"), 0, 0, 100000)
	if !ok {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid search page"})
		return
	}
	pageSize, ok := webConversationSearchInt(
		r.URL.Query().Get("page_size"),
		defaultWebConversationSearchPageSize,
		1,
		maxWebConversationSearchPageSize,
	)
	if !ok {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid search page size"})
		return
	}

	summaries, err := s.workspaceStore.ListVisibleRootCompatibilityFrameSummaries(
		r.Context(),
		userID,
		maxWebConversationSearchFrames,
		nil,
	)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to search conversations"})
		return
	}
	entries := make(map[string]*webConversationSearchEntry, len(summaries))
	orderedFrameIDs := make([]string, 0, len(summaries))
	queryFolded := strings.ToLower(query)
	terms := strings.Fields(queryFolded)
	for _, summary := range summaries {
		frame := summary.Frame
		frameID := strings.TrimSpace(frame.ID)
		if frameID == "" {
			continue
		}
		if query == "" {
			entries[frameID] = &webConversationSearchEntry{
				Summary: summary, MessageType: "text", MatchKind: "recent", ModifiedAt: frame.UpdatedAt.UnixMilli(),
			}
			orderedFrameIDs = append(orderedFrameIDs, frameID)
			continue
		}
		titleScore := webConversationSearchTextScore(frame.Name, queryFolded, terms)
		if titleScore == 0 {
			titleScore = webConversationSearchTextScore(frame.TaskSummary, queryFolded, terms)
		}
		if titleScore == 0 {
			continue
		}
		entries[frameID] = &webConversationSearchEntry{
			Summary: summary, MessageType: "text", Preview: strings.TrimSpace(frame.TaskSummary), MatchKind: "title",
			MatchCount: 1, Relevance: titleScore, ModifiedAt: frame.UpdatedAt.UnixMilli(),
		}
	}

	if query != "" {
		if s.transcriptStore == nil {
			writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "conversation search is not available"})
			return
		}
		summariesByID := make(map[string]workspace.CompatibilityFrameSummary, len(summaries))
		for _, summary := range summaries {
			summariesByID[summary.Frame.ID] = summary
		}
		candidateQuery := query
		if len(terms) > 0 {
			candidateQuery = terms[0]
		}
		hits, err := s.transcriptStore.SearchVisibleWebMessages(r.Context(), transcriptstore.SearchVisibleWebMessagesInput{
			OwnerID: userID, Query: candidateQuery, CandidateLimit: maxWebConversationSearchMessages,
		})
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to search conversation messages"})
			return
		}
		for _, hit := range hits {
			summary, visible := summariesByID[hit.RootFrameID]
			if !visible {
				continue
			}
			var message map[string]any
			if err := json.Unmarshal([]byte(hit.MessageJSON), &message); err != nil {
				continue
			}
			text := strings.TrimSpace(webMessageText(message))
			messageScore := webConversationSearchTextScore(text, queryFolded, terms)
			if messageScore == 0 {
				continue
			}
			entry := entries[hit.RootFrameID]
			if entry == nil {
				entry = &webConversationSearchEntry{
					Summary: summary, MatchKind: "message", ModifiedAt: summary.Frame.UpdatedAt.UnixMilli(),
				}
				entries[hit.RootFrameID] = entry
			}
			entry.MatchCount++
			if entry.MatchKind == "title" {
				entry.MatchKind = "title_message"
				entry.Relevance += 80
			}
			if messageScore > entry.Relevance || entry.MessageID == "" {
				entry.MessageID = firstNonEmpty(strings.TrimSpace(webString(message["msg_id"])), strings.TrimSpace(hit.MessageID))
				entry.MessageType = webConversationSearchMessageType(message)
				entry.MessageAt = webMessageCreatedAt(message)
				entry.Preview = webConversationSearchPreview(text, queryFolded, 180)
				entry.Relevance = max(entry.Relevance, messageScore)
			}
		}
	}

	results := make([]*webConversationSearchEntry, 0, len(entries))
	if query == "" {
		for _, frameID := range orderedFrameIDs {
			if entry := entries[frameID]; entry != nil {
				results = append(results, entry)
			}
		}
	} else {
		for _, entry := range entries {
			entry.Relevance += min(entry.MatchCount, 20)
			results = append(results, entry)
		}
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Relevance != results[j].Relevance {
				return results[i].Relevance > results[j].Relevance
			}
			if results[i].ModifiedAt != results[j].ModifiedAt {
				return results[i].ModifiedAt > results[j].ModifiedAt
			}
			return results[i].Summary.Frame.ID < results[j].Summary.Frame.ID
		})
	}

	total := len(results)
	start := page * pageSize
	if start > total {
		start = total
	}
	end := min(total, start+pageSize)
	items := make([]map[string]any, 0, end-start)
	for _, entry := range results[start:end] {
		items = append(items, map[string]any{
			"conversation":       webConversation(entry.Summary.Frame, entry.Summary.ProjectName),
			"message_id":         entry.MessageID,
			"message_type":       entry.MessageType,
			"message_created_at": entry.MessageAt,
			"preview_text":       entry.Preview,
			"project_name":       entry.Summary.ProjectName,
			"match_kind":         entry.MatchKind,
			"match_count":        entry.MatchCount,
			"relevance":          entry.Relevance,
		})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": pageSize, "has_more": end < total,
	})
}

func webConversationSearchInt(raw string, fallback, minimum, maximum int) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, false
	}
	return value, true
}

func webConversationSearchTextScore(value, query string, terms []string) int {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	if value == "" || query == "" {
		return 0
	}
	switch {
	case value == query:
		return 1000
	case strings.HasPrefix(value, query):
		return 820
	case strings.Contains(value, query):
		return 700
	}
	for _, term := range terms {
		if term == "" || !strings.Contains(value, term) {
			return 0
		}
	}
	if len(terms) > 0 {
		return 560
	}
	return 0
}

func webConversationSearchMessageType(message map[string]any) string {
	messageType := strings.TrimSpace(webString(message["type"]))
	switch messageType {
	case "", "message", "user_message", "assistant_message":
		return "text"
	default:
		return messageType
	}
}

func webConversationSearchPreview(value, query string, maxRunes int) string {
	compact := strings.Join(strings.Fields(value), " ")
	runes := []rune(compact)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return compact
	}
	queryRunes := []rune(query)
	lowerRunes := []rune(strings.ToLower(compact))
	match := webConversationSearchRuneIndex(lowerRunes, queryRunes)
	if match < 0 {
		match = 0
	}
	start := max(0, match-maxRunes/3)
	if start+maxRunes > len(runes) {
		start = len(runes) - maxRunes
	}
	end := min(len(runes), start+maxRunes)
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + strings.TrimSpace(string(runes[start:end])) + suffix
}

func webConversationSearchRuneIndex(value, query []rune) int {
	if len(query) == 0 || len(query) > len(value) {
		return -1
	}
	for start := 0; start <= len(value)-len(query); start++ {
		match := true
		for offset := range query {
			if value[start+offset] != query[offset] {
				match = false
				break
			}
		}
		if match {
			return start
		}
	}
	return -1
}

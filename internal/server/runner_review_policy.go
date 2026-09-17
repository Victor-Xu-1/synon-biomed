package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	"synon-go/internal/skills"
)

const (
	sessionRunnerReviewPolicySchema         = "synon.runner_review_policy.v3"
	previousSessionRunnerReviewPolicySchema = "synon.runner_review_policy.v2"
	legacySessionRunnerReviewPolicySchema   = "synon.runner_review_policy.v1"
)

type sessionRunnerResolvedReviewPolicy struct {
	Schema                         string   `json:"schema"`
	PolicyID                       string   `json:"policyId"`
	Authority                      string   `json:"authority"`
	SessionID                      string   `json:"sessionId"`
	StreamUID                      string   `json:"streamUid"`
	RunnerAttempt                  int      `json:"runnerAttempt"`
	ClaimedInputRevision           int64    `json:"claimedInputRevision"`
	TaskIntentID                   string   `json:"taskIntentId,omitempty"`
	TaskIntentRevision             int64    `json:"taskIntentRevision,omitempty"`
	RootAgent                      string   `json:"rootAgent"`
	EvidenceReviewRequired         bool     `json:"evidenceReviewRequired"`
	ScientificReviewerProfiles     []string `json:"scientificReviewerProfiles"`
	SelectedSkillNames             []string `json:"selectedSkillNames,omitempty"`
	RequiredScientificCapabilities []string `json:"requiredScientificCapabilities,omitempty"`
	ResolutionSignals              []string `json:"resolutionSignals,omitempty"`
}

// sessionRunnerResolvedReviewPolicyV2 is decode-only. Version 2 incorrectly
// bound the mutable provider/model selection into the durable task policy.
// A valid v2 checkpoint is authenticated with its original digest and then
// upgraded to v3, where switching models cannot change task identity.
type sessionRunnerResolvedReviewPolicyV2 struct {
	sessionRunnerResolvedReviewPolicy
	Model string `json:"model"`
}

type sessionRunnerReviewPolicyInput struct {
	SessionID                      string
	StreamUID                      string
	RunnerAttempt                  int
	ClaimedInputRevision           int64
	TaskIntentID                   string
	TaskIntentRevision             int64
	RootAgent                      string
	UserRequestedEvidenceReview    bool
	SelectedSkillNames             []string
	RequiredScientificCapabilities []string
	TrustedScientificSignals       []string
}

var sessionRunnerFixedJobAgents = foldedSet(
	"ONBOARDING",
	"REVIEWER",
	"BOOKMARKER",
)

func resolveSessionRunnerReviewPolicy(input sessionRunnerReviewPolicyInput) (sessionRunnerResolvedReviewPolicy, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.RootAgent = normalizeBundledAgentName(input.RootAgent)
	if input.SessionID == "" || input.StreamUID == "" || input.RunnerAttempt <= 0 || input.ClaimedInputRevision <= 0 || input.RootAgent == "" {
		return sessionRunnerResolvedReviewPolicy{}, errors.New("review policy requires exact runner, task, and agent authority")
	}

	skills := uniqueSortedFolded(input.SelectedSkillNames)
	capabilities := uniqueSortedScientificCapabilities(input.RequiredScientificCapabilities)
	trustedSignals := normalizeTrustedScientificReviewSignals(input.TrustedScientificSignals)
	signals := append([]string{"agent:" + input.RootAgent}, trustedSignals...)
	// Automatic completion review has one authority: the conversation's
	// verifier_mode switch. Scientific tools, Skills, agents, and source signals
	// enrich an enabled review but can never turn it on behind the user's back.
	evidenceRequired := input.UserRequestedEvidenceReview

	policy := sessionRunnerResolvedReviewPolicy{
		Schema:                         sessionRunnerReviewPolicySchema,
		Authority:                      "server",
		SessionID:                      input.SessionID,
		StreamUID:                      input.StreamUID,
		RunnerAttempt:                  input.RunnerAttempt,
		ClaimedInputRevision:           input.ClaimedInputRevision,
		TaskIntentID:                   strings.TrimSpace(input.TaskIntentID),
		TaskIntentRevision:             input.TaskIntentRevision,
		RootAgent:                      input.RootAgent,
		EvidenceReviewRequired:         evidenceRequired,
		ScientificReviewerProfiles:     nil,
		SelectedSkillNames:             skills,
		RequiredScientificCapabilities: capabilities,
		ResolutionSignals:              uniqueSortedFolded(signals),
	}
	policy.PolicyID = sessionRunnerReviewPolicyID(policy)
	if err := validateSessionRunnerResolvedReviewPolicy(policy); err != nil {
		return sessionRunnerResolvedReviewPolicy{}, err
	}
	return policy, nil
}

func validateSessionRunnerResolvedReviewPolicy(policy sessionRunnerResolvedReviewPolicy) error {
	if policy.Schema != sessionRunnerReviewPolicySchema || policy.Authority != "server" ||
		strings.TrimSpace(policy.SessionID) == "" || strings.TrimSpace(policy.StreamUID) == "" ||
		policy.RunnerAttempt <= 0 || policy.ClaimedInputRevision <= 0 ||
		strings.TrimSpace(policy.RootAgent) == "" {
		return errors.New("resolved review policy identity is invalid")
	}
	if len(policy.ScientificReviewerProfiles) > 4 {
		return errors.New("resolved review policy exceeds the reviewer-profile bound")
	}
	if len(policy.ScientificReviewerProfiles) > 0 && !policy.EvidenceReviewRequired {
		return errors.New("scientific review cannot bypass evidence review")
	}
	for _, profile := range policy.ScientificReviewerProfiles {
		if strings.TrimSpace(profile) == "" || foldedSetContains(sessionRunnerFixedJobAgents, profile) {
			return errors.New("resolved scientific reviewer profile is invalid")
		}
	}
	want := sessionRunnerReviewPolicyID(policy)
	if strings.TrimSpace(policy.PolicyID) == "" || !strings.EqualFold(policy.PolicyID, want) {
		return errors.New("resolved review policy digest is invalid")
	}
	return nil
}

func sessionRunnerReviewPolicyID(policy sessionRunnerResolvedReviewPolicy) string {
	digest := sha256.New()
	for _, value := range []string{
		policy.Schema,
		policy.Authority,
		strings.TrimSpace(policy.SessionID),
		strings.TrimSpace(policy.StreamUID),
		fmt.Sprint(policy.RunnerAttempt),
		fmt.Sprint(policy.ClaimedInputRevision),
		strings.TrimSpace(policy.TaskIntentID),
		fmt.Sprint(policy.TaskIntentRevision),
		normalizeBundledAgentName(policy.RootAgent),
		fmt.Sprint(policy.EvidenceReviewRequired),
		strings.Join(uniqueSortedFolded(policy.ScientificReviewerProfiles), "\x1f"),
		strings.Join(uniqueSortedFolded(policy.SelectedSkillNames), "\x1f"),
		strings.Join(uniqueSortedScientificCapabilities(policy.RequiredScientificCapabilities), "\x1f"),
		strings.Join(uniqueSortedFolded(policy.ResolutionSignals), "\x1f"),
	} {
		writeSessionReviewDigestString(digest, value)
	}
	return "review-policy-" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func sessionRunnerReviewPolicyV2ID(policy sessionRunnerResolvedReviewPolicyV2) string {
	digest := sha256.New()
	for _, value := range []string{
		policy.Schema,
		policy.Authority,
		strings.TrimSpace(policy.SessionID),
		strings.TrimSpace(policy.StreamUID),
		fmt.Sprint(policy.RunnerAttempt),
		fmt.Sprint(policy.ClaimedInputRevision),
		strings.TrimSpace(policy.TaskIntentID),
		fmt.Sprint(policy.TaskIntentRevision),
		normalizeBundledAgentName(policy.RootAgent),
		strings.TrimSpace(policy.Model),
		fmt.Sprint(policy.EvidenceReviewRequired),
		strings.Join(uniqueSortedFolded(policy.ScientificReviewerProfiles), "\x1f"),
		strings.Join(uniqueSortedFolded(policy.SelectedSkillNames), "\x1f"),
		strings.Join(uniqueSortedScientificCapabilities(policy.RequiredScientificCapabilities), "\x1f"),
		strings.Join(uniqueSortedFolded(policy.ResolutionSignals), "\x1f"),
	} {
		writeSessionReviewDigestString(digest, value)
	}
	return "review-policy-" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func migrateSessionRunnerReviewPolicyV2(encoded []byte) (sessionRunnerResolvedReviewPolicy, error) {
	var legacy sessionRunnerResolvedReviewPolicyV2
	if err := json.Unmarshal(encoded, &legacy); err != nil ||
		legacy.Schema != previousSessionRunnerReviewPolicySchema ||
		legacy.Authority != "server" || strings.TrimSpace(legacy.SessionID) == "" ||
		strings.TrimSpace(legacy.StreamUID) == "" || legacy.RunnerAttempt <= 0 ||
		legacy.ClaimedInputRevision <= 0 || strings.TrimSpace(legacy.RootAgent) == "" ||
		strings.TrimSpace(legacy.Model) == "" ||
		!strings.EqualFold(strings.TrimSpace(legacy.PolicyID), sessionRunnerReviewPolicyV2ID(legacy)) {
		return sessionRunnerResolvedReviewPolicy{}, errors.New("review-policy v2 checkpoint is invalid")
	}
	upgraded := legacy.sessionRunnerResolvedReviewPolicy
	upgraded.Schema = sessionRunnerReviewPolicySchema
	upgraded.PolicyID = sessionRunnerReviewPolicyID(upgraded)
	if err := validateSessionRunnerResolvedReviewPolicy(upgraded); err != nil {
		return sessionRunnerResolvedReviewPolicy{}, errors.New("review-policy v2 checkpoint is invalid")
	}
	return upgraded, nil
}

func sessionRunnerReviewPolicyFromEntries(entries []eventjournal.Entry, run *sessionRunnerChatRun) (sessionRunnerResolvedReviewPolicy, bool, error) {
	if run == nil || run.Transcript == nil {
		return sessionRunnerResolvedReviewPolicy{}, false, nil
	}
	for index := len(entries) - 1; index >= 0; index-- {
		policy, present, err := sessionRunnerReviewPolicyFromEntry(entries[index])
		if err != nil {
			return sessionRunnerResolvedReviewPolicy{}, false, err
		}
		if !present {
			continue
		}
		if !sessionRunnerReviewPolicyMatchesRun(policy, run) {
			continue
		}
		return policy, true, nil
	}
	return sessionRunnerResolvedReviewPolicy{}, false, nil
}

func sessionRunnerReviewPolicyForLogicalInputFromEntries(
	entries []eventjournal.Entry,
	run *sessionRunnerChatRun,
) (sessionRunnerResolvedReviewPolicy, bool, error) {
	if run == nil || run.Transcript == nil {
		return sessionRunnerResolvedReviewPolicy{}, false, nil
	}
	for index := len(entries) - 1; index >= 0; index-- {
		policy, present, err := sessionRunnerReviewPolicyFromEntry(entries[index])
		if err != nil {
			return sessionRunnerResolvedReviewPolicy{}, false, err
		}
		if !present || policy.RunnerAttempt >= run.Attempt {
			continue
		}
		if policy.StreamUID == run.Transcript.Stream.UID && policy.SessionID == run.SessionID &&
			policy.ClaimedInputRevision == run.Transcript.Claim.ClaimedInputRevision &&
			policy.TaskIntentID == run.TaskIntentID && policy.TaskIntentRevision == run.TaskIntentRevision {
			return policy, true, nil
		}
	}
	return sessionRunnerResolvedReviewPolicy{}, false, nil
}

func sessionRunnerReviewPolicyFromEntry(
	entry eventjournal.Entry,
) (sessionRunnerResolvedReviewPolicy, bool, error) {
	message := entry.Message
	if stringValue(message["type"]) != "runner_checkpoint" ||
		stringValue(message["status"]) != "completed" ||
		stringValue(message["toolPhase"]) != "review_policy" {
		return sessionRunnerResolvedReviewPolicy{}, false, nil
	}
	raw, found := message["resolvedReviewPolicy"]
	if !found {
		return sessionRunnerResolvedReviewPolicy{}, false, errors.New("review-policy checkpoint is missing its server policy")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return sessionRunnerResolvedReviewPolicy{}, false, errors.New("review-policy checkpoint cannot be decoded")
	}
	var envelope struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return sessionRunnerResolvedReviewPolicy{}, false, errors.New("review-policy checkpoint cannot be decoded")
	}
	// v1 policies predate task-intent-scoped scientific signals. They may have
	// inherited a source connector from an older user request, so they are
	// intentionally not restored or carried. A valid v2 policy is safe to
	// migrate because its old digest is verified before the mutable model field
	// is removed from the policy identity.
	if envelope.Schema == legacySessionRunnerReviewPolicySchema {
		return sessionRunnerResolvedReviewPolicy{}, false, nil
	}
	if envelope.Schema == previousSessionRunnerReviewPolicySchema {
		policy, err := migrateSessionRunnerReviewPolicyV2(encoded)
		if err != nil {
			return sessionRunnerResolvedReviewPolicy{}, false, err
		}
		return policy, true, nil
	}
	if envelope.Schema != sessionRunnerReviewPolicySchema {
		return sessionRunnerResolvedReviewPolicy{}, false, errors.New("review-policy checkpoint has an unsupported schema")
	}
	var policy sessionRunnerResolvedReviewPolicy
	if err := json.Unmarshal(encoded, &policy); err != nil || validateSessionRunnerResolvedReviewPolicy(policy) != nil {
		return sessionRunnerResolvedReviewPolicy{}, false, errors.New("review-policy checkpoint is invalid")
	}
	return policy, true, nil
}

func rebindSessionRunnerReviewPolicyForRetry(
	policy sessionRunnerResolvedReviewPolicy,
	run *sessionRunnerChatRun,
) (sessionRunnerResolvedReviewPolicy, error) {
	if run == nil || run.Transcript == nil {
		return sessionRunnerResolvedReviewPolicy{}, errors.New("review-policy retry requires current runner authority")
	}
	policy.RunnerAttempt = run.Attempt
	policy.ClaimedInputRevision = run.Transcript.Claim.ClaimedInputRevision
	policy.TaskIntentID = run.TaskIntentID
	policy.TaskIntentRevision = run.TaskIntentRevision
	policy.PolicyID = sessionRunnerReviewPolicyID(policy)
	if err := validateSessionRunnerResolvedReviewPolicy(policy); err != nil {
		return sessionRunnerResolvedReviewPolicy{}, err
	}
	return policy, nil
}

func sessionRunnerReviewPolicyMatchesRun(policy sessionRunnerResolvedReviewPolicy, run *sessionRunnerChatRun) bool {
	if run == nil || run.Transcript == nil {
		return false
	}
	return policy.StreamUID == run.Transcript.Stream.UID && policy.RunnerAttempt == run.Attempt &&
		policy.ClaimedInputRevision == run.Transcript.Claim.ClaimedInputRevision &&
		policy.SessionID == run.SessionID && policy.TaskIntentID == run.TaskIntentID &&
		policy.TaskIntentRevision == run.TaskIntentRevision
}

func sessionRunnerReviewPolicySkillNames(selected []skills.Skill) []string {
	result := make([]string, 0, len(selected))
	for _, skill := range selected {
		result = append(result, skill.Name)
	}
	return uniqueSortedFolded(result)
}

func projectSessionRunnerReviewPolicy(session sessionstore.Session, policy sessionRunnerResolvedReviewPolicy) (sessionstore.Session, error) {
	if err := validateSessionRunnerResolvedReviewPolicy(policy); err != nil {
		return session, err
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return session, err
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		return session, err
	}
	orchestration := copyMapAny(session.Orchestration)
	if orchestration == nil {
		orchestration = map[string]any{}
	}
	config, _ := orchestration["sessionConfig"].(map[string]any)
	config = copyMapAny(config)
	if config == nil {
		config = map[string]any{}
	}
	config["resolvedReviewPolicy"] = projected
	orchestration["sessionConfig"] = config
	session.Orchestration = orchestration
	return session, nil
}

func clearSessionRunnerReviewPolicyProjection(session sessionstore.Session) sessionstore.Session {
	orchestration := copyMapAny(session.Orchestration)
	if orchestration == nil {
		return session
	}
	config, _ := orchestration["sessionConfig"].(map[string]any)
	config = copyMapAny(config)
	if config == nil {
		return session
	}
	delete(config, "resolvedReviewPolicy")
	orchestration["sessionConfig"] = config
	session.Orchestration = orchestration
	return session
}

func (s *Server) resolveSessionRunnerReviewPolicyForRun(
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	entries []eventjournal.Entry,
	selectedSkills []skills.Skill,
	run *sessionRunnerChatRun,
) (sessionstore.Session, error) {
	if run == nil || run.Transcript == nil {
		return session, nil
	}
	if !sessionRunnerVerificationEnabled(session) {
		run.ReviewPolicy = nil
		run.VerificationExplicitlyDisabled = true
		return clearSessionRunnerReviewPolicyProjection(session), nil
	}
	// An enabled user-selected verifier mode survives retry and continuation
	// boundaries through the exact transcript/task policy identity.
	if restored, found, err := sessionRunnerReviewPolicyFromEntries(entries, run); err != nil {
		return session, err
	} else if found {
		if !sessionRunnerReviewPolicyRequired(restored) {
			return session, nil
		}
		projected, err := projectSessionRunnerReviewPolicy(session, restored)
		if err != nil {
			return session, err
		}
		run.ReviewPolicy = &restored
		return projected, nil
	}
	if prior, found, err := sessionRunnerReviewPolicyForLogicalInputFromEntries(entries, run); err != nil {
		return session, err
	} else if found {
		carried, err := rebindSessionRunnerReviewPolicyForRetry(prior, run)
		if err != nil {
			return session, err
		}
		if !sessionRunnerReviewPolicyRequired(carried) {
			return session, nil
		}
		projectedPolicy, err := sessionRunnerReviewPolicyProjection(carried)
		if err != nil {
			return session, err
		}
		if err := s.checkpointChatTool(
			options, run, "completed", "server review policy carried across same-input retry",
			carried.PolicyID, "review_policy", map[string]any{"resolvedReviewPolicy": projectedPolicy},
		); err != nil {
			return session, fmt.Errorf("checkpoint carried review policy: %w", err)
		}
		projected, err := projectSessionRunnerReviewPolicy(session, carried)
		if err != nil {
			return session, err
		}
		run.ReviewPolicy = &carried
		return projected, nil
	}

	if s.workspaceStore == nil {
		return session, errors.New("workspace store is required for review-policy resolution")
	}
	frame, found, err := s.workspaceStore.GetFrame(session.ID)
	if err != nil {
		return session, err
	}
	if !found {
		if !sessionRunnerVerificationEnabled(session) {
			return session, nil
		}
		return session, fmt.Errorf("review-policy frame %q does not exist", session.ID)
	}
	requiredCapabilities := run.requiredScientificCapabilitiesSnapshot()
	trustedScientificSignals := run.trustedScientificReviewSignalsSnapshot()
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: session.ID, StreamUID: run.Transcript.Stream.UID,
		RunnerAttempt: run.Attempt, ClaimedInputRevision: run.Transcript.Claim.ClaimedInputRevision,
		TaskIntentID: run.TaskIntentID, TaskIntentRevision: run.TaskIntentRevision,
		RootAgent:                      frame.AgentName,
		UserRequestedEvidenceReview:    sessionRunnerVerificationEnabled(session),
		SelectedSkillNames:             sessionRunnerReviewPolicySkillNames(selectedSkills),
		RequiredScientificCapabilities: requiredCapabilities,
		TrustedScientificSignals:       trustedScientificSignals,
	})
	if err != nil {
		return session, err
	}
	if !sessionRunnerReviewPolicyRequired(policy) {
		return session, nil
	}
	projectedPolicy, err := sessionRunnerReviewPolicyProjection(policy)
	if err != nil {
		return session, err
	}
	if err := s.checkpointChatTool(
		options, run, "completed", "server review policy resolved", policy.PolicyID, "review_policy",
		map[string]any{"resolvedReviewPolicy": projectedPolicy},
	); err != nil {
		return session, fmt.Errorf("checkpoint resolved review policy: %w", err)
	}
	projected, err := projectSessionRunnerReviewPolicy(session, policy)
	if err != nil {
		return session, err
	}
	run.ReviewPolicy = &policy
	return projected, nil
}

// refreshSessionRunnerReviewPolicyForCompletion enriches an already-enabled
// review with trusted Skill, capability, and source/tool signals that actually
// executed. These signals never enable automatic review themselves.
func (s *Server) refreshSessionRunnerReviewPolicyForCompletion(
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
) (sessionstore.Session, error) {
	if run == nil || run.Transcript == nil {
		return session, nil
	}
	if !sessionRunnerVerificationEnabled(session) {
		run.ReviewPolicy = nil
		run.VerificationExplicitlyDisabled = true
		return clearSessionRunnerReviewPolicyProjection(session), nil
	}
	existingSkills := []string(nil)
	existingCapabilities := []string(nil)
	userRequestedEvidenceReview := true
	if run.ReviewPolicy != nil {
		existingSkills = append(existingSkills, run.ReviewPolicy.SelectedSkillNames...)
		existingCapabilities = append(existingCapabilities, run.ReviewPolicy.RequiredScientificCapabilities...)
	}
	skillNames := uniqueSortedFolded(append(existingSkills, run.executedSkillNamesSnapshot()...))
	capabilities := uniqueSortedScientificCapabilities(append(
		existingCapabilities, run.requiredScientificCapabilitiesSnapshot()...,
	))
	trustedScientificSignals := run.trustedScientificReviewSignalsSnapshot()
	if s.workspaceStore == nil {
		return session, errors.New("workspace store is required for completion review-policy resolution")
	}
	frame, found, err := s.workspaceStore.GetFrame(session.ID)
	if err != nil {
		return session, err
	}
	if !found {
		if run.ReviewPolicy == nil && !userRequestedEvidenceReview && len(capabilities) == 0 &&
			len(skillNames) == 0 && len(trustedScientificSignals) == 0 {
			return session, nil
		}
		return session, fmt.Errorf("review-policy frame %q does not exist", session.ID)
	}
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: session.ID, StreamUID: run.Transcript.Stream.UID,
		RunnerAttempt: run.Attempt, ClaimedInputRevision: run.Transcript.Claim.ClaimedInputRevision,
		TaskIntentID: run.TaskIntentID, TaskIntentRevision: run.TaskIntentRevision,
		RootAgent:                      frame.AgentName,
		UserRequestedEvidenceReview:    userRequestedEvidenceReview,
		SelectedSkillNames:             skillNames,
		RequiredScientificCapabilities: capabilities,
		TrustedScientificSignals:       trustedScientificSignals,
	})
	if err != nil {
		return session, err
	}
	if !sessionRunnerReviewPolicyRequired(policy) {
		return session, nil
	}
	if run.ReviewPolicy != nil && run.ReviewPolicy.PolicyID == policy.PolicyID {
		return session, nil
	}
	projectedPolicy, err := sessionRunnerReviewPolicyProjection(policy)
	if err != nil {
		return session, err
	}
	if err := s.checkpointChatTool(
		options, run, "completed", "server review policy refreshed after trusted Skill execution",
		policy.PolicyID, "review_policy", map[string]any{"resolvedReviewPolicy": projectedPolicy},
	); err != nil {
		return session, fmt.Errorf("checkpoint refreshed review policy: %w", err)
	}
	projected, err := projectSessionRunnerReviewPolicy(session, policy)
	if err != nil {
		return session, err
	}
	run.ReviewPolicy = &policy
	return projected, nil
}

func (s *Server) validateSessionRunnerReviewPolicyProfiles(policy sessionRunnerResolvedReviewPolicy) error {
	if err := validateSessionRunnerResolvedReviewPolicy(policy); err != nil {
		return err
	}
	if len(policy.ScientificReviewerProfiles) == 0 {
		return nil
	}
	if s == nil || s.agentCatalog == nil || s.agentCatalogError != nil {
		return errors.New("bundled agent catalog is unavailable for scientific review")
	}
	for _, name := range policy.ScientificReviewerProfiles {
		agent, found := s.agentCatalog.Agent(name)
		if !found || !agent.Enabled {
			return fmt.Errorf("scientific reviewer profile %q is unavailable or disabled", name)
		}
	}
	return nil
}

func sessionRunnerReviewPolicyRequired(policy sessionRunnerResolvedReviewPolicy) bool {
	return policy.EvidenceReviewRequired || len(policy.ScientificReviewerProfiles) > 0
}

func sessionRunnerReviewPolicyProjection(policy sessionRunnerResolvedReviewPolicy) (map[string]any, error) {
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		return nil, err
	}
	return projected, nil
}

func foldedSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func foldedSetContains(values map[string]struct{}, value string) bool {
	_, found := values[strings.ToLower(strings.TrimSpace(value))]
	return found
}

func uniqueSortedFolded(values []string) []string {
	seen := make(map[string]string, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, found := seen[key]; !found {
			seen[key] = value
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

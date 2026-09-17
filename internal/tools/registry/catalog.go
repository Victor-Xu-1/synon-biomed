package registry

import (
	"synon-go/internal/synonlink"
	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/articlefulltext"
	"synon-go/internal/tools/bindingmode"
	"synon-go/internal/tools/patentsearch"
	"synon-go/internal/tools/webfetch"
	"synon-go/internal/tools/websearch"
)

func nativeWebSearchTool(name, description string) Tool {
	return Tool{
		Name:         name,
		Description:  description,
		Capabilities: []string{"web", "search", "source-discovery", "current-information", "source-evidence", "native-web-search-contract", "read-only", "idempotent-read"},
		Input: map[string]Field{
			"query": {Type: "string", Required: true},
			"query_variants": {Type: "array", Required: false, Schema: map[string]any{
				"maxItems":    6,
				"items":       map[string]any{"type": "string", "minLength": 2},
				"description": "Alternative language or domain-specific phrasings for the same subject. For public discovery, include both Chinese and English variants when meaningful; exact identifiers remain unchanged. All variants are searched as one ranked, deduplicated discovery operation.",
			}},
			"allowed_domains": {Type: "array", Required: false},
			"blocked_domains": {Type: "array", Required: false},
			"max_results": {Type: "number", Required: false, Schema: map[string]any{
				"minimum": 1, "maximum": 200, "default": 50,
				"description": "Ranked, deduplicated candidate sources to return. Omit it for the 50-source discovery default, choose a smaller window for an exact lookup, or raise it up to 200 for broad review. This controls discovery breadth, not the number of sources that must be read or cited.",
			}},
			"published_after": {Type: "string", Required: false, Schema: map[string]any{
				"pattern":     `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`,
				"description": "Optional earliest publication date (YYYY-MM-DD). Applied only by providers that support a publication-date filter; each provider reports whether it applied the filter.",
			}},
			"published_before": {Type: "string", Required: false, Schema: map[string]any{
				"pattern":     `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`,
				"description": "Optional latest publication date (YYYY-MM-DD). Applied only by providers that support a publication-date filter; retrieval time and website chrome dates never substitute for publication time.",
			}},
			"sort": {Type: "string", Required: false, Schema: map[string]any{
				"enum": []string{"relevance", "published_desc"}, "default": "relevance",
				"description": "Preferred order. published_desc is applied only by providers with an exact publication-date sort; unsupported providers remain visible and report that the sort was not applied.",
			}},
			"provider_continuations": {Type: "object", Required: false, Schema: map[string]any{
				"maxProperties":        8,
				"additionalProperties": map[string]any{"type": "string", "minLength": 1, "maxLength": websearch.MaxProviderContinuationBytes},
				"description":          "Opaque provider cursors returned by an earlier single-query call. Do not combine this with query_variants. Providers without cursor support report that continuation was not applied.",
			}},
		},
		Executable: true,
	}
}

func nativeWebResearchTool(name, description string) Tool {
	return Tool{
		Name:         name,
		Description:  description,
		Capabilities: []string{"web", "search", "fetch", "source-investigation", "source-evidence", "evidence-read", "research", "source-class:web", "native-web-research-contract", "read-only", "idempotent-read"},
		// This retained TaskRun/direct-API operation owns a deterministic compound
		// batch. It must never become a second model-controlled research loop.
		Exposure: ToolExposureHidden,
		Input: map[string]Field{
			"operation": {Type: "string", Required: true, Schema: map[string]any{
				"enum":        []string{"search", "search_and_fetch", "verify_fact", "fetch", "extract_links", "extract_tables", "fetch_pdf_text"},
				"default":     "search_and_fetch",
				"description": "Use search_and_fetch for normal research. The legacy operation research is accepted only by the server compatibility boundary and is normalized to search_and_fetch.",
			}},
			"query": {Type: "string", Required: false},
			"query_variants": {Type: "array", Required: false, Schema: map[string]any{
				"maxItems":    6,
				"items":       map[string]any{"type": "string", "minLength": 2},
				"description": "Alternative language or source-specific expressions for the same module. Active research modules supply their model-authored Chinese and English queries here so discovery, ranking, and deep-read selection share one bilingual operation.",
			}},
			"url":  {Type: "string", Required: false},
			"fact": {Type: "string", Required: false},
			"max_sources": {Type: "number", Required: false, Schema: map[string]any{
				"minimum": 1, "maximum": 20, "default": 6,
				"description": "Full-text sources to verify and synthesize. Candidate discovery is automatically broader and is not limited to this number; use 8-20 for systematic or contested questions.",
			}},
			"allowed_domains":  {Type: "array", Required: false},
			"blocked_domains":  {Type: "array", Required: false},
			"research_session": {Type: "object", Required: false},
			"quality_target": {Type: "object", Required: false, Schema: map[string]any{
				"description": "Optional source-reading target. min_fetched_sources counts successful fetches; min_independent_domains counts independent hosts; min_deep_read_sources counts complete substantive pages rather than titles, snippets, application shells, or truncated prefixes.",
			}},
			"research_depth": {Type: "string", Required: false, Schema: map[string]any{
				"enum":        []string{"focused", "deep", "systematic"},
				"description": "Declares synthesis intent for the receipt. It never turns discovery titles or snippets into read evidence; use fetched documents or source-specific full-record/full-text tools for decision-critical claims.",
			}},
		},
		Executable: true,
	}
}

func defaultCatalog() *Registry {
	registry := New([]Tool{
		{
			Name:         "web_fetch",
			Description:  "Inspect a public URL with a bounded response body. For a complete scientific file, first obtain its exact URL from an authoritative source tool, then call download_public_scientific_file; do not reconstruct a file from this result. prompt is an optional compatibility hint.",
			Capabilities: []string{"http", "web", "bounded-read", "source-locator-read", "source-evidence", "evidence-read", "source-class:web", "read-only", "idempotent-read"},
			Input: map[string]Field{
				"url":    {Type: "string", Required: true},
				"prompt": {Type: "string", Required: false},
				"limit": {Type: "number", Required: false, Schema: map[string]any{
					"minimum": 1, "maximum": webfetch.MaxResponseLimit, "default": webfetch.MaxResponseLimit,
				}},
			},
			Executable: true,
		},
		nativeWebSearchTool("web_search", "Discover current public sources when the exact URL is unknown. Cover both Chinese and English query variants for each research subject while preserving exact identifiers. The default returns up to 50 candidates; exact lookups may request fewer and broad reviews may request up to 200. Results are ranked by query coverage and source quality and deduplicated. Scholarly providers retain complete returned abstracts, identifier and publication-date metadata; these are typed abstract records, while ordinary snippets remain discovery only. Optional publication bounds, newest-first sorting, and opaque continuation cursors report the provider that actually applied them. Fetch selected records when full text, a registry record, or another primary source is material."),
		{
			Name:         "fetch_article_fulltext",
			Description:  "Resolve a DOI or PMCID through Europe PMC and read bounded open-access full-text XML. A completed result carries evidenceState=full-text-read, recordDepth=open_access_full_text, bytesRead, and complete=true; this is stronger evidence than a discovery title, search snippet, abstract record, or landing page. When full text is unavailable but Europe PMC returns the complete abstract record, the result carries status=record_available and recordDepth=abstract_record; use it only at that measured depth rather than discarding it or calling it full text. A valid article with neither record content nor open-access full text returns status=not_available instead of a tool failure; continue with another real source when material.",
			Capabilities: []string{"biomedical-literature", "full-text", "doi", "pmcid", "official-europe-pmc", "bounded-read", "source-record-read", "source-evidence", "evidence-read", "source-class:publication", "scientific-system-tool", "scientific-literature-dependency", "read-only", "idempotent-read"},
			Input: map[string]Field{
				"doi":       {Type: "string", Required: false},
				"pmcid":     {Type: "string", Required: false},
				"max_bytes": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "patent_search",
			Description:  "Search governed patent evidence across WIPO PATENTSCOPE, Google Patents, EPO Espacenet, and CNIPA, then use lookup on selected publication numbers to read record metadata, abstract, claims, focused description passages, and examples. Discovery titles and snippets are candidates, not claim-level evidence. Use 50-200 candidates for a broad prior-art review; smaller values are only for exact lookups.",
			Capabilities: []string{"patent-search", "search", "prior-art", "wipo", "google-patents", "epo", "cnipa", "official-source-links", "download-resolution", "source-record-read", "source-evidence", "evidence-read", "source-class:patent", "read-only", "idempotent-read"},
			Input: map[string]Field{
				"operation": {Type: "string", Required: true, Schema: map[string]any{
					"enum":        []string{"search", "lookup", "resolve_download"},
					"description": "Use search for discovery, lookup to read a selected publication record, and resolve_download after choosing a publication number.",
				}},
				"query": {Type: "string", Required: false},
				"query_variants": {Type: "array", Required: false, Schema: map[string]any{
					"maxItems": 6, "items": map[string]any{"type": "string", "minLength": 2},
					"description": "Chinese, English, synonym, or classification variants for the same patent question. Search variants are merged, source-balanced, and deduplicated by publication identity.",
				}},
				"publication_number": {Type: "string", Required: false},
				"sources":            {Type: "array", Required: false},
				"focus_terms": {Type: "array", Required: false, Schema: map[string]any{
					"maxItems": 16, "items": map[string]any{"type": "string", "minLength": 2},
					"description": "Terms used only during lookup to select relevant description passages and examples; abstract, claims, and record metadata are still returned independently.",
				}},
				"max_section_chars": {Type: "number", Required: false, Schema: map[string]any{
					"minimum": 4096, "maximum": 131072, "default": 32768,
				}},
				"max_results": {Type: "number", Required: false, Schema: map[string]any{
					"minimum": 1, "maximum": 200, "default": 50,
				}},
			},
			Executable: true,
		},
		nativeWebResearchTool("web_research", "Research public evidence through one layered path: discover a ranked and deduplicated candidate pool sized to the question, then fetch and verify the best independent sources. Use Chinese and English query variants across the research task. max_sources controls full-text synthesis, not candidate discovery; prioritize a smaller set of strong sources and expand only while material evidence gaps remain."),
		{
			Name:         "binding_mode_analysis",
			Description:  "Analyze ligand-protein contacts for a public PDB structure and return a residue-level 2D interaction SVG with source evidence.",
			Capabilities: []string{"structural-biology", "protein-ligand", "pdb", "binding-contacts", "interaction-diagram", "source-evidence", "scientific-system-tool", "read-only"},
			Input: map[string]Field{
				"pdb_id":          {Type: "string", Required: false},
				"pdbId":           {Type: "string", Required: false},
				"structure_id":    {Type: "string", Required: false},
				"ligand_resname":  {Type: "string", Required: false},
				"ligand":          {Type: "string", Required: false},
				"chain":           {Type: "string", Required: false},
				"max_contacts":    {Type: "number", Required: false},
				"cutoff_angstrom": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "LSP",
			Description:  "Run original-compatible code intelligence operations through configured external stdio or socket LSP servers, with compact Go static fallback and runtime status metadata.",
			Capabilities: []string{"code-intelligence", "external-stdio-lsp", "external-socket-lsp", "static-symbol-index", "static-lsp-fallback", "lsp-runtime-status", "references", "diagnostics", "passive-lsp-diagnostics", "original-lsp-contract", "read-only"},
			Input: map[string]Field{
				"operation": {Type: "string", Required: true},
				"filePath":  {Type: "string", Required: false},
				"line":      {Type: "number", Required: false},
				"character": {Type: "number", Required: false},
				"query":     {Type: "string", Required: false},
				"newName":   {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "VisualReview",
			Description:  "Canonical top-level tool for auditable image quality when the user explicitly requests independent visual review, verifier_mode is on, or the server requires visual_artifact_validation. Do not invoke it solely because a routine task produced PNG, chart, or HTML output; use deterministic renderer and file-integrity validation for those deliverables. For requested semantic inspection, use collect/run_loop with image_path(s), inspect the attached image plus visual challenge, then record_assessment. For required review of generated labeled plots or diagrams, export a renderer-derived synon.visual-layout.v1 JSON manifest (image_path, image_sha256, canvas width/height, and text_boxes with id/text/x0/y0/x1/y1), then call validate_layout with render_manifest_path; repair every reported overlap or clipped box before delivery. Do not call or emulate host.view_image from repl.",
			Capabilities: []string{"visual-review", "image-evidence", "ui-audit", "original-visualreview-contract", "read-only"},
			Input: map[string]Field{
				"action":                   {Type: "string", Required: false},
				"review_id":                {Type: "string", Required: false},
				"objective":                {Type: "string", Required: true},
				"source":                   {Type: "string", Required: false},
				"image_paths":              {Type: "array", Required: false},
				"image_path":               {Type: "string", Required: false},
				"expected_image_paths":     {Type: "array", Required: false},
				"screenshot_command":       {Type: "string", Required: false},
				"url":                      {Type: "string", Required: false},
				"pdf_path":                 {Type: "string", Required: false},
				"ppt_path":                 {Type: "string", Required: false},
				"document_path":            {Type: "string", Required: false},
				"smiles":                   {Type: "string", Required: false},
				"reference_image_paths":    {Type: "array", Required: false},
				"expected_text":            {Type: "array", Required: false},
				"expected_smiles":          {Type: "string", Required: false},
				"expected_visual_contract": {Type: "array", Required: false},
				"max_review_rounds":        {Type: "number", Required: false},
				"enable_ocr":               {Type: "boolean", Required: false},
				"enable_layout_diff":       {Type: "boolean", Required: false},
				"assessment":               {Type: "object", Required: false},
				"max_pages":                {Type: "number", Required: false},
				"render_output_dir":        {Type: "string", Required: false},
				"render_manifest_path":     {Type: "string", Required: false},
				"viewports":                {Type: "array", Required: false},
				"criteria":                 {Type: "array", Required: false},
				"style_profile":            {Type: "string", Required: false},
				"allow_synonlink":          {Type: "boolean", Required: false},
				"max_images":               {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "synon_link",
			Description:  "Route browser actions through the Synon Link API.",
			Capabilities: []string{"browser-bridge"},
			Input: map[string]Field{
				"userId":   {Type: "string", Required: true},
				"clientId": {Type: "string", Required: true},
				"action":   {Type: "string", Required: true, Schema: map[string]any{"enum": synonlink.ActionNames()}},
				"payload":  {Type: "object", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_store",
			Description:  "Persist and list session metadata in the Go runtime store.",
			Capabilities: []string{"json-session-index", "session-metadata"},
			Input: map[string]Field{
				"sessionId": {Type: "string", Required: true},
				"title":     {Type: "string", Required: false},
				"workDir":   {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_list",
			Description:  "List durable session metadata from the Go runtime store.",
			Capabilities: []string{"json-session-index", "session-metadata", "durable-state"},
			Input:        map[string]Field{},
			Executable:   true,
		},
		{
			Name:         "session_get",
			Description:  "Read one durable session metadata record from the Go runtime store.",
			Capabilities: []string{"json-session-index", "session-metadata", "durable-state"},
			Input: map[string]Field{
				"sessionId": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "session_replay",
			Description:  "Replay durable session events from the JSONL event journal after a cursor.",
			Capabilities: []string{"jsonl-session-event-journal", "cursor-replay", "durable-state"},
			Input: map[string]Field{
				"sessionId":    {Type: "string", Required: true},
				"afterEventId": {Type: "number", Required: false},
				"limit":        {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_append",
			Description:  "Append a visible event to an existing durable session journal.",
			Capabilities: []string{"jsonl-session-event-journal", "session-metadata", "durable-state"},
			Input: map[string]Field{
				"sessionId":       {Type: "string", Required: true},
				"role":            {Type: "string", Required: true},
				"message":         {Type: "object", Required: true},
				"runId":           {Type: "string", Required: false},
				"clientMessageId": {Type: "string", Required: false},
				"runnerId":        {Type: "string", Required: false},
				"runnerAttempt":   {Type: "number", Required: false},
				"claimToken":      {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_claim",
			Description:  "Claim a durable session runner lease so one worker owns the next handoff window.",
			Capabilities: []string{"session-runner-lease", "handoff", "durable-state"},
			Input: map[string]Field{
				"sessionId":  {Type: "string", Required: true},
				"runnerId":   {Type: "string", Required: true},
				"ttlSeconds": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_heartbeat",
			Description:  "Renew an owned durable session runner lease.",
			Capabilities: []string{"session-runner-lease", "handoff", "durable-state"},
			Input: map[string]Field{
				"sessionId":     {Type: "string", Required: true},
				"runnerId":      {Type: "string", Required: true},
				"runnerAttempt": {Type: "number", Required: true},
				"claimToken":    {Type: "string", Required: true},
				"ttlSeconds":    {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_release",
			Description:  "Release an owned durable session runner lease.",
			Capabilities: []string{"session-runner-lease", "handoff", "durable-state"},
			Input: map[string]Field{
				"sessionId":     {Type: "string", Required: true},
				"runnerId":      {Type: "string", Required: true},
				"runnerAttempt": {Type: "number", Required: true},
				"claimToken":    {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "session_runner_checkpoint",
			Description:  "Record progress for the currently leased durable session runner.",
			Capabilities: []string{"session-runner-lease", "runner-checkpoint", "handoff", "durable-state"},
			Input: map[string]Field{
				"sessionId":       {Type: "string", Required: true},
				"runnerId":        {Type: "string", Required: true},
				"runnerAttempt":   {Type: "number", Required: true},
				"claimToken":      {Type: "string", Required: true},
				"status":          {Type: "string", Required: true},
				"message":         {Type: "string", Required: false},
				"afterEventId":    {Type: "number", Required: false},
				"runId":           {Type: "string", Required: false},
				"clientMessageId": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "session_runner_next",
			Description:  "Claim or renew a session runner lease and return the next durable event replay window.",
			Capabilities: []string{"session-runner-lease", "cursor-replay", "handoff", "durable-state"},
			Input: map[string]Field{
				"sessionId":    {Type: "string", Required: true},
				"runnerId":     {Type: "string", Required: true},
				"ttlSeconds":   {Type: "number", Required: false},
				"afterEventId": {Type: "number", Required: false},
				"limit":        {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_runner_finish",
			Description:  "Record a terminal outcome for the currently leased session runner and expire the lease.",
			Capabilities: []string{"session-runner-lease", "runner-finish", "handoff", "durable-state"},
			Input: map[string]Field{
				"sessionId":       {Type: "string", Required: true},
				"runnerId":        {Type: "string", Required: true},
				"runnerAttempt":   {Type: "number", Required: true},
				"claimToken":      {Type: "string", Required: true},
				"status":          {Type: "string", Required: true},
				"message":         {Type: "string", Required: false},
				"afterEventId":    {Type: "number", Required: false},
				"runId":           {Type: "string", Required: false},
				"clientMessageId": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "session_runner_pick",
			Description:  "Claim or renew the next pending durable session for a runner queue worker.",
			Capabilities: []string{"session-runner-lease", "runner-queue", "cursor-replay", "handoff", "durable-state"},
			Input: map[string]Field{
				"runnerId":     {Type: "string", Required: true},
				"ttlSeconds":   {Type: "number", Required: false},
				"afterEventId": {Type: "number", Required: false},
				"limit":        {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_runner_queue",
			Description:  "Summarize durable session runner queue depth, active leases, expired leases, and oldest pending work.",
			Capabilities: []string{"session-runner-lease", "runner-queue", "observability", "durable-state"},
			Input:        map[string]Field{},
			Executable:   true,
		},
		{
			Name:         "session_runner_backlog",
			Description:  "Return a priority-ordered durable session runner backlog with per-session state, project context, lease metadata, and next action hints.",
			Capabilities: []string{"session-runner-lease", "runner-queue", "runner-backlog", "multi-session-planning", "observability", "durable-state"},
			Input: map[string]Field{
				"limit":           {Type: "number", Required: false},
				"includeRunning":  {Type: "boolean", Required: false},
				"includeTerminal": {Type: "boolean", Required: false},
				"projectId":       {Type: "string", Required: false},
				"state":           {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_bind_project",
			Description:  "Bind an existing durable session to a project directory inside the configured file root.",
			Capabilities: []string{"session-project-binding", "bounded-root", "durable-state"},
			Input: map[string]Field{
				"sessionId":   {Type: "string", Required: true},
				"projectId":   {Type: "string", Required: true},
				"projectName": {Type: "string", Required: false},
				"path":        {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "session_fork",
			Description:  "Create a new durable session by cloning the source session metadata and event journal.",
			Capabilities: []string{"json-session-index", "jsonl-session-event-journal", "session-branching", "durable-state"},
			Input: map[string]Field{
				"sessionId":       {Type: "string", Required: true},
				"sourceSessionId": {Type: "string", Required: true},
				"title":           {Type: "string", Required: false},
				"workDir":         {Type: "string", Required: false},
				"resetRunner":     {Type: "boolean", Required: false},
				"projectId":       {Type: "string", Required: false},
				"projectName":     {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_rewind",
			Description:  "Truncate a durable session event journal after a checkpoint event id and refresh session metadata.",
			Capabilities: []string{"jsonl-session-event-journal", "session-rewind", "durable-state"},
			Input: map[string]Field{
				"sessionId":    {Type: "string", Required: true},
				"afterEventId": {Type: "number", Required: true},
				"resetRunner":  {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_export",
			Description:  "Export durable session metadata and event journal entries as JSON, JSONL, Markdown, or plain transcript text, optionally to a bounded file path.",
			Capabilities: []string{"jsonl-session-event-journal", "session-export", "bounded-root", "durable-state"},
			Input: map[string]Field{
				"sessionId":   {Type: "string", Required: true},
				"format":      {Type: "string", Required: false},
				"outputPath":  {Type: "string", Required: false},
				"maxEntries":  {Type: "number", Required: false},
				"includeMeta": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "artifact_register",
			Description:  "Register an agent artifact file under the configured file root with digest, MIME metadata, and optional session or run linkage.",
			Capabilities: []string{"artifact-registry", "bounded-root", "sha256", "durable-state"},
			Input: map[string]Field{
				"path":        {Type: "string", Required: true},
				"kind":        {Type: "string", Required: false},
				"title":       {Type: "string", Required: false},
				"description": {Type: "string", Required: false},
				"sessionId":   {Type: "string", Required: false},
				"runId":       {Type: "string", Required: false},
				"metadata":    {Type: "object", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "artifact_list",
			Description:  "List registered agent artifacts, optionally filtered by session, run, or kind.",
			Capabilities: []string{"artifact-registry", "durable-state", "artifact-filtering"},
			Input: map[string]Field{
				"sessionId": {Type: "string", Required: false},
				"runId":     {Type: "string", Required: false},
				"kind":      {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "artifact_get",
			Description:  "Read one registered agent artifact record by artifact id.",
			Capabilities: []string{"artifact-registry", "durable-state"},
			Input: map[string]Field{
				"artifactId": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "artifact_research_audit",
			Description:  "Classify research workflow artifacts using the original Synon phase/final-report artifact contract.",
			Capabilities: []string{"artifact-registry", "research-artifacts", "phase-audit", "original-research-artifact-contract", "read-only"},
			Input: map[string]Field{
				"files":     {Type: "array", Required: false},
				"sessionId": {Type: "string", Required: false},
				"runId":     {Type: "string", Required: false},
				"kind":      {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "StructuredOutput",
			Description:  "Return final structured JSON output in the requested shape and optionally validate it against a JSON schema subset.",
			Capabilities: []string{"structured-output", "json-schema-validation", "durable-state", "read-only"},
			Input: map[string]Field{
				"schema":    {Type: "object", Required: false},
				"value":     {Type: "object", Required: false},
				"sessionId": {Type: "string", Required: false},
				"runId":     {Type: "string", Required: false},
			},
			Executable: true,
		}, {
			Name:         "EnterWorktree",
			Description:  "Create a managed isolated git worktree for the current agent session.",
			Capabilities: []string{"git-worktree", "session-workspace", "bounded-root", "durable-state"},
			Input: map[string]Field{
				"path":      {Type: "string", Required: false},
				"name":      {Type: "string", Required: false},
				"sessionId": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "ExitWorktree",
			Description:  "Exit a managed git worktree session, keeping it on disk or removing it after clean-state checks.",
			Capabilities: []string{"git-worktree", "session-workspace", "dirty-worktree-protection", "durable-state"},
			Input: map[string]Field{
				"action":          {Type: "string", Required: true},
				"discard_changes": {Type: "boolean", Required: false},
				"sessionId":       {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "session_event_journal",
			Description:  "Append and replay durable session events from JSONL storage.",
			Capabilities: []string{"jsonl-session-event-journal", "cursor-replay", "durable-state"},
			Input: map[string]Field{
				"sessionId":       {Type: "string", Required: true},
				"operation":       {Type: "string", Required: false},
				"afterEventId":    {Type: "number", Required: false},
				"limit":           {Type: "number", Required: false},
				"role":            {Type: "string", Required: false},
				"message":         {Type: "object", Required: false},
				"clientMessageId": {Type: "string", Required: false},
				"runId":           {Type: "string", Required: false},
				"runnerId":        {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "runtime_set",
			Description:  "Persist a namespaced runtime value in the local Go agent store.",
			Capabilities: []string{"runtime-kv", "durable-state"},
			Input: map[string]Field{
				"namespace": {Type: "string", Required: true},
				"key":       {Type: "string", Required: true},
				"value":     {Type: "any", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "runtime_get",
			Description:  "Read a namespaced runtime value from the local Go agent store.",
			Capabilities: []string{"runtime-kv", "durable-state"},
			Input: map[string]Field{
				"namespace": {Type: "string", Required: true},
				"key":       {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "runtime_list",
			Description:  "List namespaced runtime values from the local Go agent store; agent results bound large values to previews, and runtime_get reads exact values.",
			Capabilities: []string{"runtime-kv", "durable-state"},
			Input: map[string]Field{
				"namespace": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "runtime_delete",
			Description:  "Delete a namespaced runtime value from the local Go agent store.",
			Capabilities: []string{"runtime-kv", "durable-state"},
			Input: map[string]Field{
				"namespace": {Type: "string", Required: true},
				"key":       {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "file_list",
			Description:  "List files and directories inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "list"},
			Input: map[string]Field{
				"path": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "file_info",
			Description:  "Inspect file or directory metadata inside the configured Synon file root without returning file content.",
			Capabilities: []string{"filesystem", "bounded-root", "metadata", "hash"},
			Input: map[string]Field{
				"path": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "file_read",
			Description:  "Read a file inside the configured Synon file root with a bounded response body and metadata.",
			Capabilities: []string{"filesystem", "bounded-root", "read", "metadata", "hash"},
			Input: map[string]Field{
				"path":     {Type: "string", Required: true},
				"limit":    {Type: "number", Required: false},
				"encoding": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Read",
			Description:  "Read a file using the original Synon Read tool contract with file_path, offset, and limit.",
			Capabilities: []string{"filesystem", "bounded-root", "read", "original-tool-contract"},
			Input: map[string]Field{
				"file_path": {Type: "string", Required: true},
				"offset":    {Type: "number", Required: false},
				"limit":     {Type: "number", Required: false},
				"pages":     {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "ReadBatch",
			Description:  "Read several small text files using the original Synon ReadBatch tool contract.",
			Capabilities: []string{"filesystem", "bounded-root", "read", "batch-read", "original-tool-contract"},
			Input: map[string]Field{
				"file_paths":         {Type: "array", Required: true},
				"max_bytes_per_file": {Type: "number", Required: false},
				"max_files":          {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_write",
			Description:  "Write UTF-8 or base64 content to a file inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "write"},
			Input: map[string]Field{
				"path":      {Type: "string", Required: true},
				"content":   {Type: "string", Required: true},
				"encoding":  {Type: "string", Required: false},
				"overwrite": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Write",
			Description:  "Create or overwrite a file using the original Synon Write tool contract.",
			Capabilities: []string{"filesystem", "bounded-root", "write", "original-tool-contract"},
			Input: map[string]Field{
				"file_path": {Type: "string", Required: true},
				"content":   {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "NotebookEdit",
			Description:  "Replace, insert, or delete a Jupyter notebook cell using the original Synon NotebookEdit tool contract.",
			Capabilities: []string{"filesystem", "bounded-root", "write", "jupyter-notebook", "original-tool-contract"},
			Input: map[string]Field{
				"notebook_path": {Type: "string", Required: true},
				"cell_id":       {Type: "string", Required: false},
				"new_source":    {Type: "string", Required: true},
				"cell_type":     {Type: "string", Required: false},
				"edit_mode":     {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_mkdir",
			Description:  "Create a directory inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "write", "directory-management"},
			Input: map[string]Field{
				"path":      {Type: "string", Required: true},
				"recursive": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_copy",
			Description:  "Copy a file or, with recursive=true, a directory inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "copy", "directory-management"},
			Input: map[string]Field{
				"path":       {Type: "string", Required: true},
				"targetPath": {Type: "string", Required: true},
				"overwrite":  {Type: "boolean", Required: false},
				"recursive":  {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_move",
			Description:  "Move or rename a file or directory inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "move", "directory-management"},
			Input: map[string]Field{
				"path":       {Type: "string", Required: true},
				"targetPath": {Type: "string", Required: true},
				"overwrite":  {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_delete",
			Description:  "Delete a file or, with recursive=true, a directory inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "delete", "directory-management"},
			Input: map[string]Field{
				"path":      {Type: "string", Required: true},
				"recursive": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_search",
			Description:  "Search text files inside the configured Synon file root. The default discovery window is 100 matches. The result reports appliedLimit and truncated; increase the limit or narrow the query whenever truncated is true instead of treating the first page as complete.",
			Capabilities: []string{"filesystem", "bounded-root", "search"},
			Input: map[string]Field{
				"path":  {Type: "string", Required: true},
				"query": {Type: "string", Required: true},
				"limit": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Glob",
			Description:  "Find files by glob pattern inside the configured Synon file root using the original Synon tool contract. The default discovery window is 100 files and the result reports truncation; raise limit or narrow the pattern when the catalogue is incomplete.",
			Capabilities: []string{"filesystem", "bounded-root", "glob-search"},
			Input: map[string]Field{
				"pattern": {Type: "string", Required: true},
				"path":    {Type: "string", Required: false},
				"limit":   {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Grep",
			Description:  "Search files by regular expression with files, content, or count output modes using the original Synon tool contract. Use head_limit with offset to page through broad matches instead of treating a bounded first window as exhaustive.",
			Capabilities: []string{"filesystem", "bounded-root", "grep-search"},
			Input: map[string]Field{
				"pattern":     {Type: "string", Required: true},
				"path":        {Type: "string", Required: false},
				"glob":        {Type: "string", Required: false},
				"output_mode": {Type: "string", Required: false},
				"-B":          {Type: "number", Required: false},
				"-A":          {Type: "number", Required: false},
				"-C":          {Type: "number", Required: false},
				"context":     {Type: "number", Required: false},
				"-n":          {Type: "boolean", Required: false},
				"-i":          {Type: "boolean", Required: false},
				"type":        {Type: "string", Required: false},
				"head_limit":  {Type: "number", Required: false},
				"offset":      {Type: "number", Required: false},
				"multiline":   {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_replace",
			Description:  "Replace exact text inside a file under the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "edit"},
			Input: map[string]Field{
				"path": {Type: "string", Required: true},
				"old":  {Type: "string", Required: true},
				"new":  {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "Edit",
			Description:  "Replace text inside a file using the original Synon Edit tool contract.",
			Capabilities: []string{"filesystem", "bounded-root", "edit", "original-tool-contract"},
			Input: map[string]Field{
				"file_path":   {Type: "string", Required: true},
				"old_string":  {Type: "string", Required: true},
				"new_string":  {Type: "string", Required: true},
				"replace_all": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "file_patch",
			Description:  "Apply structured line-based patch operations inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "structured-edit"},
			Input: map[string]Field{
				"path":       {Type: "string", Required: true},
				"operations": {Type: "array", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "Patch",
			Description:  "Dry-run or apply a unified diff using the original Synon Patch tool contract.",
			Capabilities: []string{"filesystem", "bounded-root", "unified-diff", "original-tool-contract"},
			Input: map[string]Field{
				"patch":   {Type: "string", Required: true},
				"dry_run": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "json_patch",
			Description:  "Apply structured JSON Pointer add, replace, and remove operations inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "structured-json-edit"},
			Input: map[string]Field{
				"path":       {Type: "string", Required: true},
				"operations": {Type: "array", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "code_index",
			Description:  "Index code symbols inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "code-index"},
			Input: map[string]Field{
				"path":        {Type: "string", Required: true},
				"limit":       {Type: "number", Required: false},
				"extensions":  {Type: "array", Required: false},
				"symbolKinds": {Type: "array", Required: false},
				"query":       {Type: "string", Required: false},
				"symbolLimit": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "code_references",
			Description:  "Find bounded symbol references inside the configured Synon file root.",
			Capabilities: []string{"filesystem", "bounded-root", "code-references"},
			Input: map[string]Field{
				"path":         {Type: "string", Required: true},
				"symbol":       {Type: "string", Required: true},
				"limit":        {Type: "number", Required: false},
				"extensions":   {Type: "array", Required: false},
				"contextLines": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "shell_exec",
			Description:  "Execute a command directly inside the configured Synon file root with timeout and captured output.",
			Capabilities: []string{"process", "bounded-root", "direct-exec"},
			Input: map[string]Field{
				"command": {Type: "string", Required: true},
				"args":    {Type: "array", Required: false},
				"workdir": {Type: "string", Required: false},
				"timeout": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Bash",
			Description:  "Execute an original-compatible Bash command string inside the configured Synon file root.",
			Capabilities: []string{"process", "bounded-root", "shell-command", "bash"},
			Input: map[string]Field{
				"command":           {Type: "string", Required: true},
				"workdir":           {Type: "string", Required: false},
				"timeout":           {Type: "number", Required: false},
				"description":       {Type: "string", Required: false},
				"run_in_background": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Shell",
			Description:  "Route an original-compatible shell command to Bash or PowerShell inside the configured Synon file root.",
			Capabilities: []string{"process", "bounded-root", "shell-command", "platform-shell", "shell-router", "original-shell-contract"},
			Input: map[string]Field{
				"shell":             {Type: "string", Required: false},
				"command":           {Type: "string", Required: true},
				"workdir":           {Type: "string", Required: false},
				"timeout":           {Type: "number", Required: false},
				"description":       {Type: "string", Required: false},
				"run_in_background": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "powershell",
			Description:  "Execute an original-compatible PowerShell command string inside the configured Synon file root.",
			Capabilities: []string{"process", "bounded-root", "shell-command", "powershell"},
			Input: map[string]Field{
				"command":           {Type: "string", Required: true},
				"workdir":           {Type: "string", Required: false},
				"timeout":           {Type: "number", Required: false},
				"description":       {Type: "string", Required: false},
				"run_in_background": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Sleep",
			Description:  "Wait for a bounded duration without occupying a shell process.",
			Capabilities: []string{"runtime-wait", "cancelable", "bounded-duration"},
			Input: map[string]Field{
				"durationMs":   {Type: "number", Required: false},
				"duration_ms":  {Type: "number", Required: false},
				"milliseconds": {Type: "number", Required: false},
				"seconds":      {Type: "number", Required: false},
				"duration":     {Type: "number", Required: false},
				"unit":         {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "ask_user",
			Description:  "Ask one structured question and wait when two or more viable choices materially change the scientific route, evidence or data boundary, resources, cost, risk, external action, or deliverable. Present two to four mutually exclusive options with one recommendation, substantive trade-offs, and the canonical typed evidence fields. For a substantial scientific-compute choice, bind every option to one concrete checked or provisionable implementation and give a compact observed or official CPU, memory, and GPU/VRAM profile; never offer a broad method family that the runtime can later map to a different engine. Mark unchecked facts unresolved instead of using vague resource language. Put a material fee or data-transfer boundary in the limitation only when it changes the decision. Keep user objective/scientific decision evidence separate from execution readiness: user input and ordinary completed tool calls never prove that software, services, credentials, or compute are ready. Only an enabled compute-provider reference can establish configured, and only a service-issued typed readiness attestation can establish verified_ready. Use exact current-task identifiers only in evidence arrays and keep internal execution identifiers out of user-facing prose. Do not repeat an answered decision, interrupt for a reversible routine detail, or ask the user to diagnose ordinary failures.",
			Capabilities: []string{"user-interaction", "questionnaire", "read-only"},
			Input: map[string]Field{
				"question": {Type: "string", Required: true, Schema: map[string]any{
					"minLength": 4, "pattern": `\S`,
				}},
				"header": {Type: "string", Required: true, Schema: map[string]any{
					"minLength": 2, "pattern": `\S`,
				}},
				"options":           {Type: "array", Required: true, Schema: askUserOptionsSchema()},
				"multi_select":      {Type: "boolean", Required: false},
				"human_description": {Type: "string", Required: false, Schema: map[string]any{"minLength": 1}},
			},
			Executable: true,
		},
		{
			Name:         "Agent",
			Description:  "Launch an original-compatible delegated agent through the Go durable session runner.",
			Capabilities: []string{"agent", "delegation", "task-run", "durable-state", "session-runner", "original-agent-contract"},
			Input: map[string]Field{
				"description":       {Type: "string", Required: true},
				"prompt":            {Type: "string", Required: true},
				"subagent_type":     {Type: "string", Required: false},
				"subagentType":      {Type: "string", Required: false},
				"model":             {Type: "string", Required: false},
				"tool_policy":       {Type: "string", Required: false},
				"run_in_background": {Type: "boolean", Required: false},
				"name":              {Type: "string", Required: false},
				"team_name":         {Type: "string", Required: false},
				"mode":              {Type: "string", Required: false},
				"isolation":         {Type: "string", Required: false},
				"cwd":               {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "CronCreate",
			Description:  "Create an original-compatible scheduled prompt job with a 5-field local cron expression.",
			Capabilities: []string{"scheduled-tasks", "cron", "durable-state", "original-cron-contract"},
			Input: map[string]Field{
				"cron":      {Type: "string", Required: true},
				"prompt":    {Type: "string", Required: true},
				"recurring": {Type: "boolean", Required: false},
				"durable":   {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "CronUpdate",
			Description:  "Update an original-compatible scheduled prompt job by id.",
			Capabilities: []string{"scheduled-tasks", "cron", "durable-state", "original-cron-contract"},
			Input: map[string]Field{
				"id":             {Type: "string", Required: true},
				"cron":           {Type: "string", Required: false},
				"prompt":         {Type: "string", Required: false},
				"name":           {Type: "string", Required: false},
				"description":    {Type: "string", Required: false},
				"folder":         {Type: "string", Required: false},
				"model":          {Type: "string", Required: false},
				"permissionMode": {Type: "string", Required: false},
				"worktree":       {Type: "boolean", Required: false},
				"recurring":      {Type: "boolean", Required: false},
				"frequency":      {Type: "string", Required: false},
				"scheduledTime":  {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "CronList",
			Description:  "List original-compatible scheduled prompt jobs.",
			Capabilities: []string{"scheduled-tasks", "cron", "durable-state", "read-only", "original-cron-contract"},
			Input:        map[string]Field{},
			Executable:   true,
		},
		{
			Name:         "CronDelete",
			Description:  "Delete an original-compatible scheduled prompt job by id.",
			Capabilities: []string{"scheduled-tasks", "cron", "durable-state", "original-cron-contract"},
			Input: map[string]Field{
				"id": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "cron_tick",
			Description:  "Scan scheduled cron jobs, enqueue due prompts through TaskRun, delete one-shot jobs, and stamp recurring jobs as fired.",
			Capabilities: []string{"scheduled-tasks", "cron", "task-run", "durable-state", "scheduler-tick"},
			Input: map[string]Field{
				"now":   {Type: "string", Required: false},
				"limit": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "TeamCreate",
			Description:  "Create an original-compatible multi-agent team record with a team file and lead agent id.",
			Capabilities: []string{"team", "agent-swarm", "durable-state", "original-team-contract"},
			Input: map[string]Field{
				"team_name":   {Type: "string", Required: true},
				"description": {Type: "string", Required: false},
				"agent_type":  {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "TeamDelete",
			Description:  "Delete the current original-compatible multi-agent team record and clean up its team file.",
			Capabilities: []string{"team", "agent-swarm", "durable-state", "original-team-contract"},
			Input:        map[string]Field{},
			Executable:   true,
		},
		{
			Name:         "TaskRun",
			Description:  "Compatibility boundary for executing an explicit durable task graph through the canonical Go session runner. Planning remains owned by generate_plan.",
			Capabilities: []string{"tasks", "task-run", "durable-state", "session-runner", "taskrun-advance-action", "taskrun-monitor", "taskrun-monitor-self-check", "taskrun-repair-policy", "compatibility-boundary"},
			Input: map[string]Field{
				"action":           {Type: "string", Required: false},
				"run_id":           {Type: "string", Required: false},
				"objective":        {Type: "string", Required: false},
				"success_criteria": {Type: "array", Required: false},
				"constraints":      {Type: "array", Required: false},
				"executor_scope":   {Type: "array", Required: false},
				"task_graph":       {Type: "object", Required: false},
				"message":          {Type: "string", Required: false},
			},
			Executable: true,
		}, {
			Name:         "task_create",
			Description:  "Create a durable local agent task.",
			Capabilities: []string{"tasks", "durable-state"},
			Input: map[string]Field{
				"title": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "TaskCreate",
			Description:  "Create an original-compatible structured task in the task list.",
			Capabilities: []string{"tasks", "durable-state", "original-task-contract"},
			Input: map[string]Field{
				"subject":     {Type: "string", Required: true},
				"description": {Type: "string", Required: true},
				"activeForm":  {Type: "string", Required: false},
				"metadata":    {Type: "object", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "TaskGet",
			Description:  "Retrieve one delegated/background structured task by its task-* ID. This tool does not read Synon Biomed project IDs or UUID-shaped frame/conversation IDs; those identities are supplied by the trusted runtime context.",
			Capabilities: []string{"tasks", "durable-state", "original-task-contract", "read-only"},
			Input: map[string]Field{
				"taskId": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "task_update",
			Description:  "Update a durable local agent task title or status.",
			Capabilities: []string{"tasks", "durable-state"},
			Input: map[string]Field{
				"id":     {Type: "string", Required: true},
				"title":  {Type: "string", Required: false},
				"status": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "TaskUpdate",
			Description:  "Update an original-compatible structured task, including status, ownership, metadata, and dependencies.",
			Capabilities: []string{"tasks", "durable-state", "original-task-contract"},
			Input: map[string]Field{
				"taskId":       {Type: "string", Required: true},
				"subject":      {Type: "string", Required: false},
				"description":  {Type: "string", Required: false},
				"activeForm":   {Type: "string", Required: false},
				"status":       {Type: "string", Required: false},
				"addBlocks":    {Type: "array", Required: false},
				"addBlockedBy": {Type: "array", Required: false},
				"owner":        {Type: "string", Required: false},
				"metadata":     {Type: "object", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "task_list",
			Description:  "List durable local agent tasks.",
			Capabilities: []string{"tasks", "durable-state"},
			Input:        map[string]Field{},
			Executable:   true,
		},
		{
			Name:         "TaskList",
			Description:  "List delegated/background structured tasks with task-* IDs and blocked-by state. This is not the Synon Biomed project or conversation list and must not be used to validate UUID-shaped frame IDs.",
			Capabilities: []string{"tasks", "durable-state", "original-task-contract", "read-only"},
			Input:        map[string]Field{},
			Executable:   true,
		},
		{
			Name:         "TaskOutput",
			Description:  "Read current output metadata for a durable Go task, with optional bounded wait for completion.",
			Capabilities: []string{"tasks", "task-output", "durable-state", "original-task-output-contract"},
			Input: map[string]Field{
				"task_id": {Type: "string", Required: true},
				"block":   {Type: "boolean", Required: false},
				"timeout": {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "TaskStop",
			Description:  "Stop a running durable Go task by ID, matching the original TaskStop output contract.",
			Capabilities: []string{"tasks", "task-stop", "durable-state", "original-task-stop-contract"},
			Input: map[string]Field{
				"task_id":  {Type: "string", Required: false},
				"shell_id": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "TodoWrite",
			Description:  "Replace the structured todo list for the current agent session. Every item must include content, activeForm, and a status of pending, in_progress, or completed.",
			Capabilities: []string{"todos", "session-state", "durable-state"},
			Input: map[string]Field{
				"todos":     {Type: "array", Required: true, Schema: todoWriteListSchema()},
				"sessionId": {Type: "string", Required: false},
				"agentId":   {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "settings_set",
			Description:  "Persist a local runtime setting.",
			Capabilities: []string{"settings", "durable-state"},
			Input: map[string]Field{
				"key":   {Type: "string", Required: true},
				"value": {Type: "any", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "settings_get",
			Description:  "Read a local runtime setting.",
			Capabilities: []string{"settings", "durable-state"},
			Input: map[string]Field{
				"key": {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "settings_list",
			Description:  "List all local runtime settings.",
			Capabilities: []string{"settings", "durable-state"},
			Input:        map[string]Field{},
			Executable:   true,
		},
		{
			Name:         "Config",
			Description:  "Get or set an original-compatible supported Synon setting.",
			Capabilities: []string{"settings", "durable-state", "original-config-contract"},
			Input: map[string]Field{
				"setting": {Type: "string", Required: true},
				"value":   {Type: "any", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "Compact",
			Description:  "Record an original-compatible compact handoff snapshot with PreCompact and PostCompact hook execution.",
			Capabilities: []string{"compact", "session-state", "durable-state", "hooks", "original-compact-contract"},
			Input: map[string]Field{
				"sessionId":    {Type: "string", Required: true},
				"instructions": {Type: "string", Required: false},
				"trigger":      {Type: "string", Required: false},
				"limit":        {Type: "number", Required: false},
			},
			Executable: true,
		},
		{
			Name:         toolcontract.SearchSkills,
			Description:  "Search or select SKILL.md based runtime capabilities. Results are relevance-ranked metadata only; load a chosen result by invoking the Skill tool with its name, never by reading a host filesystem path. The default page is 50 candidates; use max_results up to 200 and offset to continue whenever the result is truncated. prefix is optional and scopes by skill-name prefix or catalog namespace/directory segment; omit it when uncertain.",
			Capabilities: []string{"skill-registry", "skill-search", "read-only", "original-skillsearch-contract"},
			Input: map[string]Field{
				"query":  {Type: "string", Required: false},
				"prefix": {Type: "string", Required: false},
				"max_results": {Type: "number", Required: false, Schema: map[string]any{
					"minimum": 1, "maximum": 200, "default": 50,
				}},
				"offset": {Type: "number", Required: false, Schema: map[string]any{
					"minimum": 0, "default": 0,
				}},
			},
			Executable: true,
		},
		{
			Name:         toolcontract.Skill,
			Description:  "Load one exact Skill inline by name; this call never executes the requested task or a command. args only substitutes arguments explicitly declared by that Skill. After loading, use the returned allowedTools and preferredExecutionAssets rather than calling Skill again with a shell command. For mcp-* connector Skills, filter returns only matching live methods and is ignored for ordinary Skills.",
			Capabilities: []string{"skill-registry", "skill-invocation", "inline-skill-context", "original-skill-contract"},
			Input: map[string]Field{
				"skill":  {Type: "string", Required: true},
				"args":   {Type: "string", Required: false},
				"filter": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "AgentRuntimeDoctor",
			Description:  "Audit Go agent runtime parity against the original Synon runtime contract and report missing, partial, and passing runtime areas.",
			Capabilities: []string{"diagnostics", "agent-runtime", "query-engine", "parity-audit", "original-synon-contract"},
			Input: map[string]Field{
				"scope": {Type: "string", Required: false, Schema: map[string]any{
					"enum": []string{"all", "query-engine", "model-runner", "tool-gateway", "permissions", "settings-storage", "kernel-compute", "compute", "molecular-docking", "hooks", "skills", "sessions", "artifact-system", "workspace-worktree", "compact-memory", "taskrun-agent", "mcp", "synon-link-im"},
				}},
			},
			Executable: true,
		},
		{
			Name:         "approval_remembered_list",
			Description:  "List persisted remembered approval decisions for agent runtime and Synon Link permission governance.",
			Capabilities: []string{"permissions", "approval", "remembered-decisions", "diagnostics", "read-only"},
			Input: map[string]Field{
				"source": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "approval_remembered_revoke",
			Description:  "Revoke a persisted remembered approval decision by exact key or unique action.",
			Capabilities: []string{"permissions", "approval", "remembered-decisions", "state-mutation"},
			Input: map[string]Field{
				"key":      {Type: "string", Required: false},
				"action":   {Type: "string", Required: false},
				"source":   {Type: "string", Required: false},
				"userId":   {Type: "string", Required: false},
				"clientId": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "SendMessage",
			Description:  "Send a message to an original-compatible Agent/Task session by name, run id, session id, or broadcast target.",
			Capabilities: []string{"agent", "message-routing", "task-run", "session-runner", "original-sendmessage-contract"},
			Input: map[string]Field{
				"to":      {Type: "string", Required: true},
				"summary": {Type: "string", Required: false},
				"message": {Type: "any", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "SendUserMessage",
			Description:  "Send a visible message to the user with optional root-bounded file attachments.",
			Capabilities: []string{"user-message", "visible-output", "attachments", "original-brief-contract"},
			Input: map[string]Field{
				"message":     {Type: "string", Required: true},
				"attachments": {Type: "array", Required: false},
				"status": {Type: "string", Required: false, Schema: map[string]any{
					"enum": []string{"normal", "proactive"}, "default": "normal",
				}},
			},
			Executable: true,
		},
		{
			Name:         "ToolDoctor",
			Description:  "Run read-only diagnostics for Go tool registry, shell, web, task, compute, Synon Link, MCP, LSP, permission, and feature-gate scopes.",
			Capabilities: []string{"tool-diagnostics", "read-only", "original-tooldoctor-contract"},
			Input: map[string]Field{
				"scope": {Type: "string", Required: false},
				"smoke": {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "im_config",
			Description:  "Audit IM channel configuration without exposing secret values, including required fields, env/config keys, endpoint readiness, pairing state, and live-smoke commands.",
			Capabilities: []string{"im-config", "message-adapters", "read-only", "credential-audit", "live-smoke-readiness"},
			Input: map[string]Field{
				"platform": {Type: "string", Required: false},
				"smoke":    {Type: "boolean", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "ListMcpTools",
			Description:  "List configured MCP servers and tool schemas through the original Synon ListMcpTools contract.",
			Capabilities: []string{"mcp", "read-only", "tool-discovery", "stdio-json-rpc", "remote-http-sse-json-rpc", "remote-websocket-json-rpc", "original-mcp-contract"},
			Input: map[string]Field{
				"server": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "ListMcpResourcesTool",
			Description:  "List resources from configured MCP servers through stdio, HTTP/SSE, WebSocket, or SDK bridge JSON-RPC.",
			Capabilities: []string{"mcp", "read-only", "resources", "stdio-json-rpc", "remote-http-sse-json-rpc", "remote-websocket-json-rpc", "original-mcp-contract"},
			Input: map[string]Field{
				"server": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "ReadMcpResourceTool",
			Description:  "Read one resource from a configured MCP server by server name and URI.",
			Capabilities: []string{"mcp", "read-only", "resources", "original-mcp-contract"},
			Input: map[string]Field{
				"server": {Type: "string", Required: true},
				"uri":    {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "MCPTool",
			Description:  "Call a configured stdio, HTTP/SSE, WebSocket, or SDK-bridged MCP server tool without rewriting the third-party tool implementation.",
			Capabilities: []string{"mcp", "tool-execution", "stdio-json-rpc", "remote-http-sse-json-rpc", "remote-websocket-json-rpc", "original-mcp-contract"},
			Input: map[string]Field{
				"server":    {Type: "string", Required: true},
				"toolName":  {Type: "string", Required: true},
				"input":     {Type: "object", Required: false},
				"arguments": {Type: "object", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "pairing_list",
			Description:  "List durable IM paired users, optionally scoped to one platform.",
			Capabilities: []string{"im-pairing", "durable-state"},
			Input: map[string]Field{
				"platform": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "pairing_allow",
			Description:  "Allow or update one durable IM paired user.",
			Capabilities: []string{"im-pairing", "durable-state"},
			Input: map[string]Field{
				"platform":    {Type: "string", Required: true},
				"userId":      {Type: "string", Required: true},
				"displayName": {Type: "string", Required: false},
			},
			Executable: true,
		},
		{
			Name:         "pairing_revoke",
			Description:  "Revoke one durable IM paired user.",
			Capabilities: []string{"im-pairing", "durable-state"},
			Input: map[string]Field{
				"platform": {Type: "string", Required: true},
				"userId":   {Type: "string", Required: true},
			},
			Executable: true,
		},
		{
			Name:         "im_message",
			Description:  "Normalize IM messages, permissions, pairing, attachments, and platform payloads into durable live sessions.",
			Capabilities: []string{"im-formatting", "message-deduplication", "platform-parsing", "im-live-session", "durable-state"},
			Input: map[string]Field{
				"platform":          {Type: "string", Required: true},
				"chatId":            {Type: "string", Required: false},
				"chat_id":           {Type: "string", Required: false},
				"messageId":         {Type: "string", Required: false},
				"message_id":        {Type: "string", Required: false},
				"messageType":       {Type: "string", Required: false},
				"message_type":      {Type: "string", Required: false},
				"senderId":          {Type: "string", Required: false},
				"sender_id":         {Type: "string", Required: false},
				"senderOpenId":      {Type: "string", Required: false},
				"sender_open_id":    {Type: "string", Required: false},
				"userId":            {Type: "string", Required: false},
				"user_id":           {Type: "string", Required: false},
				"text":              {Type: "string", Required: false},
				"downloads":         {Type: "any", Required: false},
				"attachments":       {Type: "any", Required: false},
				"sourceEventId":     {Type: "string", Required: false},
				"source_event_id":   {Type: "string", Required: false},
				"clientMessageId":   {Type: "string", Required: false},
				"client_message_id": {Type: "string", Required: false},
				"taskTitle":         {Type: "string", Required: false},
				"task_title":        {Type: "string", Required: false},
			},
			Executable: true,
		},
	})
	registry.MustRegister(planToolDefinitions()...)
	registry.MustRegister(memoryToolDefinitions()...)
	registry.articleFulltextClient = articlefulltext.NewClient(articlefulltext.Options{})
	registry.patentSearchClient = patentsearch.NewClient(patentsearch.Options{})
	return registry
}

func todoWriteListSchema() map[string]any {
	return map[string]any{
		"description": "Complete replacement todo list. Preserve unfinished items and update their status instead of sending partial deltas.",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"minLength":   1,
					"pattern":     `\S`,
					"description": "Imperative description of the task to complete.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"pending", "in_progress", "completed"},
					"description": "Current task state. Exactly one item should normally be in_progress.",
				},
				"activeForm": map[string]any{
					"type":        "string",
					"minLength":   1,
					"pattern":     `\S`,
					"description": "Present-progress wording shown while this item is in progress, such as 'Gathering evidence'.",
				},
			},
			"required": []string{"content", "status", "activeForm"},
		},
	}
}

func askUserOptionsSchema() map[string]any {
	return map[string]any{
		"description": "Two to four genuinely viable, mutually exclusive options. Use user-facing text in the conversation language. Exactly one option must set recommended=true.",
		"minItems":    2,
		"maxItems":    4,
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"label": map[string]any{
					"type": "string", "minLength": 2, "maxLength": 80, "pattern": `\S`,
					"description": "A short user-facing option name. For scientific compute, name the concrete checked or provisionable engine/service; use an explicit method family only while its implementation remains unresolved.",
				},
				"description": map[string]any{
					"type": "string", "minLength": 4, "maxLength": 400, "pattern": `\S`,
					"description": "One concise sentence identifying the scientific method, conditioning/input, and concrete implementation when known; never use only a generic quality tier or broad tool category.",
				},
				"pros": map[string]any{
					"type": "string", "minLength": 2, "maxLength": 280, "pattern": `\S`,
					"description": "The option's principal advantage in the conversation language.",
				},
				"cons": map[string]any{
					"type": "string", "minLength": 2, "maxLength": 280, "pattern": `\S`,
					"description": "The option's important scientific or operational limitation.",
				},
				"readiness": map[string]any{
					"type": "string", "minLength": 4, "maxLength": 400, "pattern": `\S`,
					"description": "A concise readiness note retained for evidence audit. The service generates the user-visible readiness statement from readiness_status and exact receipts, so this note must not enlarge their scope.",
				},
				"readiness_status": map[string]any{
					"type": "string", "enum": []string{"not_applicable", "unverified", "configured", "verified_ready"},
					"description": "Typed execution-readiness authority. Use not_applicable for a non-operational choice, unverified without a typed readiness authority, configured only with an enabled compute-provider or configured readiness attestation, and verified_ready only with a service-issued successful current-task preflight or representative-run attestation.",
				},
				"decision_evidence": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 8, "uniqueItems": true,
					"description": "Exact references supporting task fit, scientific comparison, or the user's stated objective. user-input:current-task may support objective alignment but never execution readiness. Tool-call and compute-provider references must be exact current-task identifiers.",
					"items":       map[string]any{"type": "string", "minLength": 3, "maxLength": 180, "pattern": `\S`},
				},
				"readiness_evidence": map[string]any{
					"type": "array", "minItems": 0, "maxItems": 8, "uniqueItems": true,
					"description": "Exact typed readiness authority only: compute-provider:<exact enabled public provider name> for configured, or readiness-attestation:<exact service-issued id> for its attested status and scope. Keep this empty for not_applicable or unverified. User input, ordinary tool-call receipts, prose, generic capability claims, tool names, and invented identifiers are not readiness evidence.",
					"items":       map[string]any{"type": "string", "minLength": 3, "maxLength": 180, "pattern": `\S`},
				},
				"selection_basis": map[string]any{
					"type": "string", "enum": []string{"user_objective", "scientific_evidence", "execution_readiness", "balanced_tradeoff"},
					"description": "The principal basis for selecting this option. Scientific evidence needs a successful tool receipt in decision_evidence; execution_readiness needs a configured provider or typed readiness attestation; balanced_tradeoff needs both classes.",
				},
				"resources": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"description":          "Optional for substantial compute choices. Give exactly three concise observed or official facts: CPU, memory, and GPU/VRAM. Omit this object for choices without a meaningful compute profile; mark an unchecked value unresolved instead of inventing it.",
					"properties": map[string]any{
						"cpu": map[string]any{
							"type": "string", "minLength": 1, "maxLength": 100, "pattern": `\S`,
							"description": "CPU requirement or observed fit, or unresolved when no source or preflight establishes it.",
						},
						"memory": map[string]any{
							"type": "string", "minLength": 1, "maxLength": 100, "pattern": `\S`,
							"description": "System-memory requirement or observed fit, or unresolved when no source or preflight establishes it.",
						},
						"gpu": map[string]any{
							"type": "string", "minLength": 1, "maxLength": 100, "pattern": `\S`,
							"description": "GPU and VRAM requirement or observed fit; state remote or not required when applicable, or unresolved when unchecked.",
						},
					},
					"required": []string{"cpu", "memory", "gpu"},
				},
				"implementation": map[string]any{
					"type": "string", "minLength": 1, "maxLength": 100, "pattern": `\S`,
					"description": "Exact public engine, service, or executable implementation for this option. Required whenever resources is present. Do not put a broad method family here; the displayed selection and the implementation started after the answer must be the same.",
				},
				"execution_parameter_values": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 8,
					"description": "Typed declarations for controlled execution values proposed by this option. Include one item for every resolved-user-input evidence group whose numeric values appear in the option, using the exact evidence_group returned by execution preflight or the active capability registry. Omit this field when the option only asks the user to provide a missing value or when its numbers describe unrelated resources, versions, durations, counts, or outcomes.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"evidence_group": map[string]any{
								"type": "string", "minLength": 1, "maxLength": 100, "pattern": `^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`,
								"description": "Exact registry-defined resolved-user-input evidence group proposed by this option.",
							},
							"values": map[string]any{
								"type": "array", "minItems": 1, "maxItems": 16,
								"description": "Ordered finite numeric values for the registry parameters in this evidence group.",
								"items":       map[string]any{"type": "number"},
							},
						},
						"required": []string{"evidence_group", "values"},
					},
				},
				"expected_outcome": map[string]any{
					"type": "string", "minLength": 3, "maxLength": 400, "pattern": `\S`,
					"description": "The concrete result and user-facing deliverables this option is expected to produce.",
				},
				"selection_rationale": map[string]any{
					"type": "string", "minLength": 3, "maxLength": 400, "pattern": `\S`,
					"description": "Why this option is recommended or when it should be chosen. A recommended option must ground this rationale in the listed evidence without enlarging its scope.",
				},
				"recommended": map[string]any{
					"type": "boolean", "description": "Set true for exactly one option in the decision round.",
				},
				"metadata": map[string]any{"type": "object"},
			},
			"required": []string{
				"label", "description", "pros", "cons", "readiness", "readiness_status",
				"decision_evidence", "readiness_evidence", "selection_basis", "expected_outcome",
				"selection_rationale", "recommended",
			},
		},
	}
}

func DefaultWithArticleFulltextClient(client *articlefulltext.Client) *Registry {
	registry := Default()
	if client != nil {
		registry.articleFulltextClient = client
	}
	return registry
}

func DefaultWithBindingModeClient(client *bindingmode.Client) *Registry {
	registry := DefaultOperations()
	if client != nil {
		registry.bindingModeClient = client
	}
	return registry
}

func DefaultWithPatentSearchClient(client *patentsearch.Client) *Registry {
	registry := DefaultOperations()
	if client != nil {
		registry.patentSearchClient = client
	}
	return registry
}

// DefaultWithWebOptions preserves the production-safe defaults while allowing
// controlled transports in focused tests. Callers must not use this to weaken
// the public-network policy in production.
func DefaultWithWebOptions(fetchOptions webfetch.Options, searchOptions websearch.Options) *Registry {
	registry := Default()
	registry.webFetchOptions = fetchOptions
	registry.webSearchOptions = searchOptions
	return registry
}

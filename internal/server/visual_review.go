package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/tools/shellops"
)

const maxVisualReviewImages = 8
const maxVisualReviewImageBytes = 25 * 1024 * 1024
const maxVisualReviewTotalImageBytes = 32 * 1024 * 1024
const maxVisualReviewLayoutManifestBytes = 1024 * 1024
const maxVisualReviewLayoutTextBoxes = 4096
const visualReviewLayoutOverlapEpsilon = 0.5
const visualReviewRuntimeNamespace = "visual-reviews"
const visualReviewLayoutSchema = "synon.visual-layout.v1"
const visualReviewChallengeHashField = "visual_challenge_sha256"

type visualReviewLayoutManifest struct {
	Schema      string                      `json:"schema"`
	ImagePath   string                      `json:"image_path"`
	ImageSHA256 string                      `json:"image_sha256"`
	Canvas      visualReviewLayoutCanvas    `json:"canvas"`
	TextBoxes   []visualReviewLayoutTextBox `json:"text_boxes"`
}

type visualReviewLayoutCanvas struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type visualReviewLayoutTextBox struct {
	ID   string  `json:"id"`
	Text string  `json:"text"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

type visualReviewWorkspaceRootContextKey struct{}

// withVisualReviewWorkspaceRoot carries the server-authorized task workspace
// to VisualReview without adding a model-controlled path to the tool input.
func withVisualReviewWorkspaceRoot(ctx context.Context, root string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, visualReviewWorkspaceRootContextKey{}, strings.TrimSpace(root))
}

func visualReviewWorkspaceRoot(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	root, _ := ctx.Value(visualReviewWorkspaceRootContextKey{}).(string)
	return strings.TrimSpace(root)
}

func (s *Server) executeVisualReviewTool(ctx context.Context, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("VisualReview", input); err != nil {
		return nil, err
	}
	action := strings.TrimSpace(stringValue(input["action"]))
	if action == "" {
		action = "collect"
	}
	objective := strings.TrimSpace(stringValue(input["objective"]))
	if objective == "" {
		return nil, errors.New("VisualReview objective is required")
	}
	reviewID := strings.TrimSpace(stringValue(input["review_id"]))
	if reviewID == "" {
		reviewID = visualReviewID(objective)
	}
	base := visualReviewBaseResult(action, reviewID, objective, input)
	switch action {
	case "doctor":
		base["status"] = "partial"
		base["verdict"] = "pending_model_review"
		base["evidence_level"] = "none"
		base["blockers"] = []any{map[string]any{"kind": "missing_visual_evidence", "message": "Provide image_path/image_paths or a screenshot/render source to collect visual evidence."}}
		base["next_actions"] = []any{"collect visual evidence", "record model assessment"}
		base["render_diagnostics"] = visualReviewDependencyDoctor(ctx)
		s.persistVisualReviewRecord(base)
		return base, nil
	case "collect", "run_loop":
		evidence, blockers, screenshotCommandRan, screenshotCommandResult := s.collectVisualReviewEvidence(ctx, input)
		attachedImages := visualReviewAttachedEvidenceCount(evidence)
		base["evidence"] = evidence
		base["blockers"] = blockers
		base["model_visible_images_attached"] = float64(attachedImages)
		base["visual_verified"] = false
		if attachedImages > 0 {
			base["status"] = "ready_for_visual_inspection"
			base["evidence_level"] = "visual"
			base["verdict"] = "pending_model_review"
		} else {
			base["status"] = "partial"
			base["evidence_level"] = "none"
			base["verdict"] = "pending_model_review"
		}
		base["evidence_bundle"] = visualReviewEvidenceBundle(evidence, input)
		diagnostics := map[string]any{"attached_images": float64(attachedImages), "max_images": float64(maxVisualReviewImages), "skipped_images": float64(len(blockers)), "screenshot_command_ran": screenshotCommandRan}
		if len(screenshotCommandResult) > 0 {
			diagnostics["screenshot_command_result"] = screenshotCommandResult
		}
		base["diagnostics"] = diagnostics
		base["visual_decision"] = visualReviewDecision(attachedImages, false)
		if action == "run_loop" {
			base["loop"] = visualReviewLoopMetadata(input, attachedImages)
			base["analysis"] = visualReviewLoopAnalysis(input)
			base["inspection_request"] = "Inspect the attached visual evidence against the objective, then call VisualReview action=\"record_assessment\" before claiming pass."
		}
		s.persistVisualReviewRecord(base)
		return base, nil
	case "validate_layout":
		s.validateVisualReviewLayout(ctx, base, input)
		s.persistVisualReviewRecord(base)
		return base, nil
	case "record_assessment":
		assessment := objectMapValue(input["assessment"])
		if len(assessment) == 0 {
			return nil, errors.New("VisualReview record_assessment requires assessment")
		}
		previous, found, err := s.loadVisualReviewRecord(reviewID)
		if err != nil {
			return nil, fmt.Errorf("load VisualReview evidence: %w", err)
		}
		if !found {
			return nil, errors.New("VisualReview record_assessment requires a prior collect or run_loop result for this review_id")
		}
		if previousObjective := strings.TrimSpace(stringValue(previous["objective"])); previousObjective != objective {
			return nil, errors.New("VisualReview record_assessment objective must match the collected review")
		}
		verdict := strings.ToLower(firstNonEmpty(stringValue(assessment["verdict"]), "partial"))
		if verdict == "pass" {
			if visualReviewAttachedEvidenceCount(anySliceValue(previous["evidence"])) == 0 {
				return nil, errors.New("VisualReview cannot pass without previously attached visual evidence")
			}
			challengeHash := strings.TrimSpace(stringValue(previous[visualReviewChallengeHashField]))
			challengeResponse := strings.ToUpper(strings.TrimSpace(stringValue(assessment["visual_challenge_response"])))
			responseHash := sha256.Sum256([]byte(challengeResponse))
			if challengeHash == "" || challengeResponse == "" || subtle.ConstantTimeCompare(
				[]byte(challengeHash), []byte(hex.EncodeToString(responseHash[:])),
			) != 1 {
				return nil, errors.New("VisualReview pass requires the exact code from the model-visible visual challenge; metadata-only inspection cannot pass")
			}
			if !visualReviewAssessmentReferencesEvidence(assessment, anySliceValue(previous["evidence"])) {
				return nil, errors.New("VisualReview pass must list every attached image path in assessment.inspected_images")
			}
		}
		record := cloneVisualReviewRecord(previous)
		record["action"] = action
		record["status"] = "ready_for_visual_inspection"
		record["verdict"] = verdict
		record["evidence_level"] = "visual"
		record["visual_verified"] = verdict == "pass"
		record["assessment"] = normalizeVisualReviewAssessment(assessment)
		record["visual_decision"] = visualReviewDecision(visualReviewAttachedEvidenceCount(anySliceValue(previous["evidence"])), verdict == "pass")
		s.persistVisualReviewRecord(record)
		delete(record, visualReviewChallengeHashField)
		return record, nil
	default:
		return nil, fmt.Errorf("unsupported VisualReview action: %s", action)
	}
}

func visualReviewDependencyDoctor(ctx context.Context) map[string]any {
	dependencies := []map[string]any{
		visualReviewCommandDependency("node", "node"),
		visualReviewNodePackageDependency(ctx, "playwright"),
		visualReviewCommandDependency("python3", "python3"),
		visualReviewCommandDependency("pdftoppm", "pdftoppm"),
		visualReviewOfficeDependency(),
		visualReviewPythonImportDependency(ctx, "rdkit"),
		visualReviewCommandDependency("tesseract", "tesseract"),
	}
	missing := []any{}
	installPlan := []any{}
	for _, dep := range dependencies {
		if available, _ := dep["available"].(bool); available {
			continue
		}
		name := stringValue(dep["name"])
		missing = append(missing, name)
		installPlan = append(installPlan, visualReviewDependencyInstallPlan(name))
	}
	items := make([]any, 0, len(dependencies))
	for _, dep := range dependencies {
		items = append(items, dep)
	}
	return map[string]any{
		"dependencies":         items,
		"missing_dependencies": missing,
		"install_plan":         installPlan,
	}
}

func visualReviewCommandDependency(name string, command string) map[string]any {
	path, err := exec.LookPath(command)
	dep := map[string]any{"name": name, "available": err == nil}
	if err == nil {
		dep["detail"] = path
	}
	return dep
}

func visualReviewOfficeDependency() map[string]any {
	for _, command := range []string{"libreoffice", "soffice"} {
		if path, err := exec.LookPath(command); err == nil {
			return map[string]any{"name": "libreoffice", "available": true, "detail": path}
		}
	}
	return map[string]any{"name": "libreoffice", "available": false}
}

func visualReviewNodePackageDependency(ctx context.Context, packageName string) map[string]any {
	dep := map[string]any{"name": packageName, "available": false}
	if _, err := exec.LookPath("node"); err != nil {
		dep["detail"] = "node not found"
		return dep
	}
	output, err := visualReviewRunProbe(ctx, "node", "-e", "console.log(require.resolve('"+packageName+"'))")
	if err != nil {
		dep["detail"] = strings.TrimSpace(output)
		return dep
	}
	dep["available"] = true
	dep["detail"] = strings.TrimSpace(output)
	return dep
}

func visualReviewPythonImportDependency(ctx context.Context, moduleName string) map[string]any {
	dep := map[string]any{"name": moduleName, "available": false}
	if _, err := exec.LookPath("python3"); err != nil {
		dep["detail"] = "python3 not found"
		return dep
	}
	output, err := visualReviewRunProbe(ctx, "python3", "-c", "import "+moduleName+"; print('ok')")
	if err != nil {
		dep["detail"] = strings.TrimSpace(output)
		return dep
	}
	dep["available"] = true
	dep["detail"] = strings.TrimSpace(output)
	return dep
}

func visualReviewRunProbe(ctx context.Context, command string, args ...string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, command, args...)
	out, err := cmd.CombinedOutput()
	if probeCtx.Err() != nil {
		return string(out), probeCtx.Err()
	}
	return string(out), err
}

func visualReviewDependencyInstallPlan(name string) map[string]any {
	commands := map[string]string{
		"node":        "Install Node.js from your system package manager or https://nodejs.org/.",
		"playwright":  "npm install playwright && npx playwright install chromium",
		"python3":     "Install Python 3 from your system package manager.",
		"pdftoppm":    "sudo apt-get install poppler-utils",
		"libreoffice": "sudo apt-get install libreoffice",
		"rdkit":       "python3 -m pip install rdkit-pypi",
		"tesseract":   "sudo apt-get install tesseract-ocr",
	}
	reasons := map[string]string{
		"node":        "Required to run URL screenshot capture commands.",
		"playwright":  "Required to render URLs into screenshots.",
		"python3":     "Required for molecule rendering probes and RDKit import checks.",
		"pdftoppm":    "Required to render PDF pages and PPT-converted PDFs.",
		"libreoffice": "Required to convert PPT/PPTX files before rendering slides.",
		"rdkit":       "Required to render molecule diagrams from SMILES.",
		"tesseract":   "Required for OCR extraction from visual evidence.",
	}
	return map[string]any{"name": name, "available": false, "install_command": firstNonEmpty(commands[name], "Install "+name+" using your platform package manager."), "reason": firstNonEmpty(reasons[name], "Required by VisualReview render or OCR workflows.")}
}

func (s *Server) persistVisualReviewRecord(record map[string]any) {
	if s == nil || s.runtimeStore == nil || record == nil {
		return
	}
	reviewID := strings.TrimSpace(stringValue(record["review_id"]))
	if reviewID == "" {
		return
	}
	_, _ = s.runtimeStore.Set(visualReviewRuntimeNamespace, safeVisualReviewID(reviewID), record)
}

func (s *Server) loadVisualReviewRecord(reviewID string) (map[string]any, bool, error) {
	if s == nil || s.runtimeStore == nil {
		return nil, false, errors.New("visual review runtime store is unavailable")
	}
	entry, found, err := s.runtimeStore.Get(visualReviewRuntimeNamespace, safeVisualReviewID(reviewID))
	if err != nil || !found {
		return nil, found, err
	}
	record := mapValue(entry.Value)
	if len(record) == 0 {
		return nil, false, errors.New("visual review record is malformed")
	}
	return cloneVisualReviewRecord(record), true, nil
}

func cloneVisualReviewRecord(record map[string]any) map[string]any {
	cloned := make(map[string]any, len(record))
	for key, value := range record {
		cloned[key] = value
	}
	return cloned
}

func visualReviewAssessmentReferencesEvidence(assessment map[string]any, evidence []any) bool {
	inspected := map[string]struct{}{}
	for _, raw := range stringArrayValue(assessment["inspected_images"]) {
		path := filepath.ToSlash(strings.TrimSpace(raw))
		if path != "" {
			inspected[path] = struct{}{}
		}
	}
	required := 0
	for _, raw := range evidence {
		item := mapValue(raw)
		if stringValue(item["status"]) != "attached" || stringValue(item["source"]) != "image_path" {
			continue
		}
		required++
		path := filepath.ToSlash(strings.TrimSpace(stringValue(item["path"])))
		if _, ok := inspected[path]; !ok {
			return false
		}
	}
	return required > 0
}

func safeVisualReviewID(reviewID string) string {
	cleaned := strings.Builder{}
	for _, r := range reviewID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			cleaned.WriteRune(r)
		} else {
			cleaned.WriteRune('_')
		}
	}
	return cleaned.String()
}

func visualReviewBaseResult(action string, reviewID string, objective string, input map[string]any) map[string]any {
	return map[string]any{
		"ok":                 true,
		"action":             action,
		"status":             "partial",
		"verdict":            "pending_model_review",
		"evidence_level":     "none",
		"review_id":          reviewID,
		"visual_verified":    false,
		"objective":          objective,
		"evidence":           []any{},
		"blockers":           []any{},
		"next_actions":       []any{},
		"inspection_request": "Inspect the attached visual evidence against the objective and record an assessment before claiming pass.",
		"review_contract": map[string]any{
			"criteria":                            stringArrayValue(input["criteria"]),
			"required_checks":                     []any{"visual evidence is attached", "text is readable when expected", "layout does not overlap", "assessment is recorded"},
			"instruction":                         "Do not mark visual work as pass without model-visible evidence and a recorded assessment.",
			"cannot_pass_without_visual_evidence": true,
		},
	}
}

func (s *Server) collectVisualReviewEvidence(ctx context.Context, input map[string]any) ([]any, []any, bool, map[string]any) {
	primaryPaths := stringArrayValue(input["image_paths"])
	primaryPaths = append(primaryPaths, stringArrayValue(input["expected_image_paths"])...)
	if single := strings.TrimSpace(stringValue(input["image_path"])); single != "" {
		primaryPaths = append([]string{single}, primaryPaths...)
	}
	referencePaths := stringArrayValue(input["reference_image_paths"])
	maxImages := int(numberValue(input["max_images"]))
	if maxImages <= 0 || maxImages > maxVisualReviewImages {
		maxImages = maxVisualReviewImages
	}
	evidence := []any{}
	blockers := []any{}
	attachedImageBytes := int64(0)
	screenshotCommandRan := false
	screenshotCommandResult := map[string]any{}
	appendImage := func(rawPath string, role string) {
		if visualReviewAttachedEvidenceCount(evidence) >= maxImages {
			blockers = append(blockers, map[string]any{"kind": "image_too_large", "message": "VisualReview image limit reached", "path": rawPath})
			return
		}
		item, err := s.visualReviewImageEvidence(ctx, rawPath, role)
		if err != nil {
			blockers = append(blockers, map[string]any{"kind": "image_unavailable", "message": err.Error(), "path": rawPath})
			return
		}
		imageBytes := int64(numberValue(item["bytes"]))
		if imageBytes <= 0 || attachedImageBytes+imageBytes > maxVisualReviewTotalImageBytes {
			blockers = append(blockers, map[string]any{"kind": "image_total_too_large", "message": fmt.Sprintf("VisualReview images exceed the %d byte model attachment limit", maxVisualReviewTotalImageBytes), "path": rawPath})
			return
		}
		attachedImageBytes += imageBytes
		evidence = append(evidence, item)
	}
	if command := strings.TrimSpace(stringValue(input["screenshot_command"])); command != "" {
		screenshotCommandRan = true
		if err := shellops.CheckSafety("Shell", command, nil); err != nil {
			blockers = append(blockers, map[string]any{"kind": "screenshot_command_failed", "message": err.Error()})
		} else {
			result, err := shellops.ExecuteShellCommand(ctx, s.fileRoot, "Shell", command, stringValue(input["workdir"]), numberValue(input["timeout_ms"]))
			screenshotCommandResult = map[string]any{
				"exitCode":        float64(result.ExitCode),
				"stdoutBytes":     float64(result.StdoutBytes),
				"stderrBytes":     float64(result.StderrBytes),
				"stdoutTruncated": result.StdoutTruncated,
				"stderrTruncated": result.StderrTruncated,
			}
			if err != nil {
				blockers = append(blockers, map[string]any{"kind": "screenshot_command_failed", "message": err.Error()})
			} else {
				primaryPaths = append(primaryPaths, visualReviewImagePathsFromShellOutput(result.Stdout, result.Stderr)...)
			}
		}
	}

	for _, rawPath := range primaryPaths {
		appendImage(rawPath, "primary")
	}
	for _, rawPath := range referencePaths {
		appendImage(rawPath, "reference")
	}
	if source := strings.TrimSpace(stringValue(input["source"])); source == "synonlink_browser" || source == "synonlink_desktop" {
		if boolValue(input["allow_synonlink"], false) {
			blockers = append(blockers, map[string]any{"kind": "needs_synonlink_client", "message": "SynonLink was explicitly allowed, but VisualReview does not use it as a default capture backend. Capture an image path or provide screenshot_command for stable review."})
		} else {
			blockers = append(blockers, map[string]any{"kind": "synonlink_not_allowed", "message": "SynonLink capture is disabled by default. Provide image_paths or screenshot_command, or explicitly allow SynonLink as a separate follow-up."})
		}
	}
	if stringValue(input["url"]) != "" && strings.TrimSpace(stringValue(input["screenshot_command"])) == "" && visualReviewAttachedEvidenceCount(evidence) == 0 {
		evidence = append(evidence, map[string]any{"source": "url", "status": "planned"})
		blockers = append(blockers, map[string]any{"kind": "missing_capture_command", "message": "URL review needs real screenshots. Provide screenshot_command or captured image_paths."})
	}
	if visualReviewAttachedEvidenceCount(evidence) == 0 && len(evidence) == 0 {
		evidence = append(evidence, map[string]any{"source": "static", "status": "planned"})
		blockers = append(blockers, map[string]any{"kind": "missing_visual_evidence", "message": "No screenshot, image, render, or diagram evidence was provided."})
	}
	return evidence, blockers, screenshotCommandRan, screenshotCommandResult
}

func (s *Server) validateVisualReviewLayout(ctx context.Context, base map[string]any, input map[string]any) {
	evidence, blockers, validation := s.collectVisualReviewLayoutEvidence(ctx, input)
	passed := len(blockers) == 0
	base["evidence"] = evidence
	base["blockers"] = blockers
	base["layout_validation"] = validation
	base["layout_verified"] = passed
	base["visual_verified"] = passed
	base["verification_scope"] = []any{"image integrity", "renderer text bounds", "text overlap", "canvas clipping"}
	if passed {
		base["status"] = "passed"
		base["verdict"] = "pass"
		base["evidence_level"] = "machine_verified_layout"
		base["next_actions"] = []any{}
		base["inspection_request"] = "Layout checks passed against immutable image bytes and renderer-derived text bounds. Semantic or aesthetic claims still require collect/run_loop plus a model-visible challenge."
		base["visual_decision"] = visualReviewDecision(1, true)
		return
	}
	base["status"] = "needs_fix"
	base["verdict"] = "fail"
	base["evidence_level"] = "machine_checked_layout"
	base["next_actions"] = []any{"repair the reported overlap, clipping, integrity, or manifest defect", "render again and rerun validate_layout"}
	base["inspection_request"] = "Repair every reported deterministic layout blocker, regenerate the image and manifest, then rerun VisualReview validate_layout."
	base["visual_decision"] = map[string]any{
		"status": "needs_fix", "should_continue": true, "next_step": "repair_and_validate_layout",
		"quality_score": float64(30), "confidence_score": float64(95), "evidence_score": float64(90),
		"issue_score": float64(70), "reasons": []any{"deterministic layout validation reported blockers"},
		"recommended_actions": base["next_actions"],
	}
}

func (s *Server) collectVisualReviewLayoutEvidence(ctx context.Context, input map[string]any) ([]any, []any, map[string]any) {
	evidence := []any{}
	blockers := []any{}
	validation := map[string]any{
		"schema": visualReviewLayoutSchema, "box_count": float64(0), "overlap_count": float64(0),
		"clipped_count": float64(0), "invalid_box_count": float64(0), "overlaps": []any{}, "clipped_boxes": []any{},
	}
	manifestPath := strings.TrimSpace(stringValue(input["render_manifest_path"]))
	if manifestPath == "" {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_manifest_missing", "validate_layout requires render_manifest_path"))
		return evidence, blockers, validation
	}
	resolvedManifest, err := s.resolveVisualReviewWorkspacePath(ctx, manifestPath, "visual review layout manifest")
	if err != nil {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_manifest_unavailable", err.Error()))
		return evidence, blockers, validation
	}
	info, err := os.Stat(resolvedManifest)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > maxVisualReviewLayoutManifestBytes {
		message := fmt.Sprintf("layout manifest must be a non-empty JSON file no larger than %d bytes", maxVisualReviewLayoutManifestBytes)
		if err != nil {
			message = err.Error()
		}
		blockers = append(blockers, visualReviewLayoutBlocker("layout_manifest_invalid", message))
		return evidence, blockers, validation
	}
	raw, err := os.ReadFile(resolvedManifest)
	if err != nil {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_manifest_unavailable", err.Error()))
		return evidence, blockers, validation
	}
	manifestDigest := sha256.Sum256(raw)
	manifestEvidence := map[string]any{
		"source": "layout_manifest", "status": "validated", "path": filepath.ToSlash(manifestPath),
		"resolved_path": resolvedManifest, "bytes": float64(len(raw)), "sha256": hex.EncodeToString(manifestDigest[:]),
	}
	evidence = append(evidence, manifestEvidence)
	var manifest visualReviewLayoutManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		manifestEvidence["status"] = "rejected"
		blockers = append(blockers, visualReviewLayoutBlocker("layout_manifest_invalid", "decode layout manifest: "+err.Error()))
		return evidence, blockers, validation
	}
	if strings.TrimSpace(manifest.Schema) != visualReviewLayoutSchema {
		manifestEvidence["status"] = "rejected"
		blockers = append(blockers, visualReviewLayoutBlocker("layout_manifest_schema_invalid", fmt.Sprintf("layout manifest schema must be %q", visualReviewLayoutSchema)))
	}
	if len(manifest.TextBoxes) == 0 || len(manifest.TextBoxes) > maxVisualReviewLayoutTextBoxes {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_text_bounds_missing", fmt.Sprintf("layout manifest must contain 1-%d renderer-derived text_boxes", maxVisualReviewLayoutTextBoxes)))
	}
	validation["box_count"] = float64(len(manifest.TextBoxes))

	imagePath := strings.TrimSpace(manifest.ImagePath)
	requestedImagePath := strings.TrimSpace(stringValue(input["image_path"]))
	if imagePath == "" {
		imagePath = requestedImagePath
	}
	if imagePath == "" {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_image_missing", "layout manifest requires image_path"))
		return evidence, blockers, validation
	}
	imageEvidence, err := s.visualReviewImageEvidence(ctx, imagePath, "primary")
	if err != nil {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_image_unavailable", err.Error()))
		return evidence, blockers, validation
	}
	evidence = append([]any{imageEvidence}, evidence...)
	if requestedImagePath != "" {
		requestedResolved, resolveErr := s.resolveVisualReviewImagePath(ctx, requestedImagePath)
		if resolveErr != nil || filepath.Clean(requestedResolved) != filepath.Clean(stringValue(imageEvidence["resolved_path"])) {
			blockers = append(blockers, visualReviewLayoutBlocker("layout_image_mismatch", "input image_path and manifest image_path must resolve to the same authorized file"))
		}
	}
	manifestImageDigest := strings.ToLower(strings.TrimSpace(manifest.ImageSHA256))
	if len(manifestImageDigest) != sha256.Size*2 || manifestImageDigest != strings.ToLower(stringValue(imageEvidence["sha256"])) {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_image_hash_mismatch", "manifest image_sha256 does not match the immutable image bytes"))
	}
	width := float64(numberValue(imageEvidence["width"]))
	height := float64(numberValue(imageEvidence["height"]))
	if !visualReviewFinitePositive(manifest.Canvas.Width) || !visualReviewFinitePositive(manifest.Canvas.Height) ||
		math.Abs(manifest.Canvas.Width-width) > visualReviewLayoutOverlapEpsilon ||
		math.Abs(manifest.Canvas.Height-height) > visualReviewLayoutOverlapEpsilon {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_canvas_mismatch", fmt.Sprintf("manifest canvas %.2fx%.2f does not match decoded image %.0fx%.0f", manifest.Canvas.Width, manifest.Canvas.Height, width, height)))
	}

	seenIDs := map[string]struct{}{}
	boxesToValidate := manifest.TextBoxes
	if len(boxesToValidate) > maxVisualReviewLayoutTextBoxes {
		boxesToValidate = boxesToValidate[:maxVisualReviewLayoutTextBoxes]
	}
	validBoxes := make([]visualReviewLayoutTextBox, 0, len(boxesToValidate))
	clipped := []any{}
	clippedCount := 0
	invalidCount := 0
	for index, box := range boxesToValidate {
		box.ID = strings.TrimSpace(box.ID)
		box.Text = strings.TrimSpace(box.Text)
		if box.ID == "" || box.Text == "" || !visualReviewFinite(box.X0) || !visualReviewFinite(box.Y0) ||
			!visualReviewFinite(box.X1) || !visualReviewFinite(box.Y1) || box.X1 <= box.X0 || box.Y1 <= box.Y0 {
			invalidCount++
			continue
		}
		if _, duplicate := seenIDs[box.ID]; duplicate {
			invalidCount++
			continue
		}
		seenIDs[box.ID] = struct{}{}
		validBoxes = append(validBoxes, box)
		if box.X0 < 0 || box.Y0 < 0 || box.X1 > width || box.Y1 > height {
			clippedCount++
			if len(clipped) < 100 {
				clipped = append(clipped, map[string]any{"id": box.ID, "index": float64(index), "bounds": []any{box.X0, box.Y0, box.X1, box.Y1}})
			}
		}
	}
	validation["invalid_box_count"] = float64(invalidCount)
	validation["clipped_count"] = float64(clippedCount)
	validation["clipped_boxes"] = clipped
	if invalidCount > 0 {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_box_invalid", fmt.Sprintf("%d text boxes have missing/duplicate ids, empty text, non-finite coordinates, or non-positive bounds", invalidCount)))
	}
	if clippedCount > 0 {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_text_clipped", fmt.Sprintf("%d renderer text boxes cross the image canvas", clippedCount)))
	}

	overlaps := []any{}
	totalOverlaps := 0
	for left := 0; left < len(validBoxes); left++ {
		for right := left + 1; right < len(validBoxes); right++ {
			widthOverlap := math.Min(validBoxes[left].X1, validBoxes[right].X1) - math.Max(validBoxes[left].X0, validBoxes[right].X0)
			heightOverlap := math.Min(validBoxes[left].Y1, validBoxes[right].Y1) - math.Max(validBoxes[left].Y0, validBoxes[right].Y0)
			if widthOverlap <= visualReviewLayoutOverlapEpsilon || heightOverlap <= visualReviewLayoutOverlapEpsilon {
				continue
			}
			totalOverlaps++
			if len(overlaps) < 100 {
				overlaps = append(overlaps, map[string]any{
					"left_id": validBoxes[left].ID, "right_id": validBoxes[right].ID,
					"overlap_width": widthOverlap, "overlap_height": heightOverlap, "overlap_area": widthOverlap * heightOverlap,
				})
			}
		}
	}
	validation["overlap_count"] = float64(totalOverlaps)
	validation["overlaps"] = overlaps
	validation["image_path"] = filepath.ToSlash(imagePath)
	validation["image_sha256"] = stringValue(imageEvidence["sha256"])
	validation["canvas"] = map[string]any{"width": width, "height": height}
	if totalOverlaps > 0 {
		blockers = append(blockers, visualReviewLayoutBlocker("layout_text_overlap", fmt.Sprintf("%d pairs of renderer text boxes overlap", totalOverlaps)))
	}
	return evidence, blockers, validation
}

func visualReviewLayoutBlocker(kind string, message string) map[string]any {
	return map[string]any{"kind": kind, "message": message}
}

func visualReviewFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func visualReviewFinitePositive(value float64) bool {
	return visualReviewFinite(value) && value > 0
}

func visualReviewImagePathsFromShellOutput(stdout string, stderr string) []string {
	seen := map[string]bool{}
	paths := []string{}
	for _, token := range strings.Fields(stdout + "\n" + stderr) {
		candidate := strings.Trim(token, " \t\r\n'\"`:,;()[]{}<>")
		if candidate == "" || !visualReviewLooksLikeImagePath(candidate) || seen[candidate] {
			continue
		}
		seen[candidate] = true
		paths = append(paths, candidate)
	}
	return paths
}

func visualReviewLooksLikeImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func (s *Server) visualReviewImageEvidence(ctx context.Context, rawPath string, role string) (map[string]any, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return nil, errors.New("empty image path")
	}
	target, err := s.resolveVisualReviewImagePath(ctx, trimmed)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", rawPath)
	}
	if info.Size() > maxVisualReviewImageBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", rawPath, maxVisualReviewImageBytes)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	mimeType := visualReviewMimeType(target)
	if mimeType == "" {
		return nil, fmt.Errorf("unsupported image type for %s", rawPath)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode image %s: %w", rawPath, err)
	}
	sum := sha256.Sum256(raw)
	return map[string]any{
		"source":        "image_path",
		"status":        "attached",
		"role":          firstNonEmpty(role, "primary"),
		"path":          filepath.ToSlash(rawPath),
		"resolved_path": target,
		"mime_type":     mimeType,
		"bytes":         float64(len(raw)),
		"sha256":        hex.EncodeToString(sum[:]),
		"width":         float64(config.Width),
		"height":        float64(config.Height),
	}, nil
}

func (s *Server) resolveVisualReviewImagePath(ctx context.Context, rawPath string) (string, error) {
	return s.resolveVisualReviewWorkspacePath(ctx, rawPath, "visual review image")
}

func (s *Server) resolveVisualReviewWorkspacePath(ctx context.Context, rawPath string, kind string) (string, error) {
	roots := s.visualReviewAuthorizedRoots(ctx)
	kind = firstNonEmpty(strings.TrimSpace(kind), "visual review file")
	if len(roots) == 0 {
		return "", fmt.Errorf("%s authority is unavailable", kind)
	}

	target := filepath.FromSlash(strings.TrimSpace(rawPath))
	if filepath.IsAbs(target) {
		for _, root := range roots {
			if err := ensurePathWithinRoot(root, target, kind); err != nil {
				continue
			}
			resolved, err := visualReviewExistingPathWithinRoot(root, target, kind)
			if err != nil {
				return "", err
			}
			return resolved, nil
		}
		return "", fmt.Errorf("%s is outside the authorized task workspace and file root: %s", kind, filepath.ToSlash(target))
	}

	var firstResolveErr error
	for _, root := range roots {
		candidate := filepath.Join(root, target)
		if err := ensurePathWithinRoot(root, candidate, kind); err != nil {
			if firstResolveErr == nil {
				firstResolveErr = err
			}
			continue
		}
		resolved, err := visualReviewExistingPathWithinRoot(root, candidate, kind)
		if err == nil {
			return resolved, nil
		}
		if firstResolveErr == nil || !errors.Is(err, os.ErrNotExist) {
			firstResolveErr = err
		}
	}
	if firstResolveErr != nil && !errors.Is(firstResolveErr, os.ErrNotExist) {
		return "", fmt.Errorf("resolve %s %q: %w", kind, rawPath, firstResolveErr)
	}
	return "", fmt.Errorf("%s %q was not found in the authorized task workspace or file root", kind, rawPath)
}

func (s *Server) visualReviewAuthorizedRoots(ctx context.Context) []string {
	candidates := []string{visualReviewWorkspaceRoot(ctx)}
	if s != nil {
		candidates = append(candidates, s.fileRoot)
	}
	seen := map[string]struct{}{}
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		canonical, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			continue
		}
		canonical = filepath.Clean(canonical)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		roots = append(roots, canonical)
	}
	return roots
}

func visualReviewExistingPathWithinRoot(root string, target string, kind string) (string, error) {
	if err := ensurePathWithinRoot(root, target, kind); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if err := ensurePathWithinRoot(root, resolved, kind); err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// agentRuntimeVisualReviewRichResponse binds collected image bytes to the
// next model request. The JSON result alone is audit metadata; without these
// content parts the model cannot actually inspect the claimed visual evidence.
func (s *Server) agentRuntimeVisualReviewRichResponse(ctx context.Context, value any) (any, error) {
	payload := mapValue(value)
	if nested := mapValue(payload["result"]); len(nested) > 0 {
		payload = nested
	}
	action := strings.TrimSpace(stringValue(payload["action"]))
	if action != "collect" && action != "run_loop" {
		return value, nil
	}

	parts := []agentruntime.ContentPart{}
	var totalBytes int64
	for _, rawEvidence := range anySliceValue(payload["evidence"]) {
		evidence := mapValue(rawEvidence)
		if stringValue(evidence["status"]) != "attached" || stringValue(evidence["source"]) != "image_path" {
			continue
		}
		resolved, err := s.resolveVisualReviewImagePath(ctx, stringValue(evidence["resolved_path"]))
		if err != nil {
			return nil, fmt.Errorf("attach visual review image: %w", err)
		}
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("attach visual review image: %w", err)
		}
		if len(raw) == 0 || len(raw) > maxVisualReviewImageBytes {
			return nil, fmt.Errorf("attach visual review image %q: invalid image size", filepath.Base(resolved))
		}
		totalBytes += int64(len(raw))
		if totalBytes > maxVisualReviewTotalImageBytes {
			return nil, fmt.Errorf("visual review images exceed the %d byte model attachment limit", maxVisualReviewTotalImageBytes)
		}
		digest := sha256.Sum256(raw)
		if recorded := strings.TrimSpace(stringValue(evidence["sha256"])); recorded == "" || !strings.EqualFold(recorded, hex.EncodeToString(digest[:])) {
			return nil, fmt.Errorf("attach visual review image %q: image changed after collection", filepath.Base(resolved))
		}
		mimeType := visualReviewMimeType(resolved)
		if mimeType == "" || mimeType != stringValue(evidence["mime_type"]) {
			return nil, fmt.Errorf("attach visual review image %q: image type changed after collection", filepath.Base(resolved))
		}
		parts = append(parts, agentruntime.ContentPart{
			Type: agentruntime.ContentPartImage,
			Media: &agentruntime.MediaContent{
				MIMEType: mimeType,
				Filename: filepath.Base(resolved),
				Source:   agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: raw},
			},
		})
	}
	if len(parts) == 0 {
		return value, nil
	}
	challengeToken, err := s.nextVisualReviewChallengeToken()
	if err != nil {
		return nil, fmt.Errorf("create visual review challenge: %w", err)
	}
	challengePNG, err := visualReviewChallengePNG(challengeToken)
	if err != nil {
		return nil, fmt.Errorf("render visual review challenge: %w", err)
	}
	challenge := map[string]any{
		"required_for_pass": true,
		"image_filename":    "visual-review-challenge.png",
		"response_field":    "assessment.visual_challenge_response",
		"instruction":       "Read the 8-character code rendered in the attached visual-review-challenge.png and return it exactly in assessment.visual_challenge_response. The expected code is intentionally absent from text metadata.",
	}
	payload["visual_challenge"] = challenge
	if err := s.persistVisualReviewChallenge(payload, challengeToken); err != nil {
		return nil, err
	}
	parts = append(parts, agentruntime.ContentPart{
		Type: agentruntime.ContentPartImage,
		Media: &agentruntime.MediaContent{
			MIMEType: "image/png", Filename: "visual-review-challenge.png",
			Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: challengePNG},
		},
	})
	return agentRuntimeRichToolResponse{value: value, parts: parts}, nil
}

func (s *Server) persistVisualReviewChallenge(payload map[string]any, token string) error {
	reviewID := strings.TrimSpace(stringValue(payload["review_id"]))
	if reviewID == "" {
		return errors.New("visual review challenge requires review_id")
	}
	record, found, err := s.loadVisualReviewRecord(reviewID)
	if err != nil {
		return fmt.Errorf("load visual review challenge record: %w", err)
	}
	if !found {
		record = cloneVisualReviewRecord(payload)
	}
	digest := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(token))))
	record[visualReviewChallengeHashField] = hex.EncodeToString(digest[:])
	record["visual_challenge"] = payload["visual_challenge"]
	if _, err := s.runtimeStore.Set(visualReviewRuntimeNamespace, safeVisualReviewID(reviewID), record); err != nil {
		return fmt.Errorf("persist visual review challenge: %w", err)
	}
	return nil
}

func (s *Server) nextVisualReviewChallengeToken() (string, error) {
	if s != nil && s.visualReviewChallengeToken != nil {
		return normalizeVisualReviewChallengeToken(s.visualReviewChallengeToken())
	}
	const alphabet = "23456789ABCDEF"
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	token := make([]byte, len(random))
	for index, value := range random {
		token[index] = alphabet[int(value)%len(alphabet)]
	}
	return string(token), nil
}

func normalizeVisualReviewChallengeToken(token string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	token = strings.ToUpper(strings.TrimSpace(token))
	if len(token) != 8 {
		return "", errors.New("visual review challenge token must contain exactly 8 characters")
	}
	for _, character := range token {
		if !strings.ContainsRune("23456789ABCDEF", character) {
			return "", fmt.Errorf("visual review challenge contains unsupported character %q", character)
		}
	}
	return token, nil
}

func visualReviewChallengePNG(token string) ([]byte, error) {
	token, err := normalizeVisualReviewChallengeToken(token, nil)
	if err != nil {
		return nil, err
	}
	const scale = 10
	const margin = 18
	const glyphWidth = 5
	const glyphHeight = 7
	const glyphGap = 1
	width := margin*2 + (len(token)*glyphWidth+(len(token)-1)*glyphGap)*scale
	height := margin*2 + glyphHeight*scale
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	background := color.RGBA{R: 250, G: 250, B: 250, A: 255}
	foreground := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	border := color.RGBA{R: 120, G: 120, B: 120, A: 255}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.SetRGBA(x, y, background)
		}
	}
	for x := 0; x < width; x++ {
		canvas.SetRGBA(x, 0, border)
		canvas.SetRGBA(x, height-1, border)
	}
	for y := 0; y < height; y++ {
		canvas.SetRGBA(0, y, border)
		canvas.SetRGBA(width-1, y, border)
	}
	for characterIndex, character := range token {
		glyph, ok := visualReviewChallengeGlyphs[character]
		if !ok {
			return nil, fmt.Errorf("missing visual challenge glyph %q", character)
		}
		xOffset := margin + characterIndex*(glyphWidth+glyphGap)*scale
		for row, pattern := range glyph {
			for column, pixel := range pattern {
				if pixel != '1' {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						canvas.SetRGBA(xOffset+column*scale+dx, margin+row*scale+dy, foreground)
					}
				}
			}
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

var visualReviewChallengeGlyphs = map[rune][7]string{
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "10000", "11110", "00001", "00001", "11110"},
	'6': {"01110", "10000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00001", "01110"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01111", "10000", "10000", "10000", "10000", "10000", "01111"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
}

func visualReviewAttachedEvidenceCount(evidence []any) int {
	count := 0
	for _, raw := range evidence {
		item := mapValue(raw)
		if stringValue(item["status"]) == "attached" {
			count++
		}
	}
	return count
}

func visualReviewMimeType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func visualReviewEvidenceBundle(evidence []any, input map[string]any) map[string]any {
	images := []any{}
	references := []any{}
	for _, raw := range evidence {
		item := mapValue(raw)
		if stringValue(item["status"]) != "attached" {
			continue
		}
		if stringValue(item["role"]) == "reference" {
			references = append(references, raw)
		} else {
			images = append(images, raw)
		}
	}
	return map[string]any{
		"visual_evidence_count":    float64(len(images)),
		"reference_evidence_count": float64(len(references)),
		"images":                   images,
		"references":               references,
		"expected_text":            stringArrayValue(input["expected_text"]),
		"expected_smiles":          stringValue(input["expected_smiles"]),
		"expected_contract":        stringArrayValue(input["expected_visual_contract"]),
		"max_review_rounds":        float64(maxInt(1, int(numberValue(input["max_review_rounds"])))),
	}
}

func visualReviewLoopMetadata(input map[string]any, evidenceCount int) map[string]any {
	status := "needs_evidence"
	if evidenceCount > 0 {
		status = "waiting_for_model_assessment"
	}
	return map[string]any{
		"enabled":    true,
		"status":     status,
		"max_rounds": float64(maxInt(1, int(numberValue(input["max_review_rounds"])))),
		"round":      float64(1),
		"next_step":  visualReviewDecision(evidenceCount, false)["next_step"],
	}
}

func visualReviewLoopAnalysis(input map[string]any) map[string]any {
	kind := "visual"
	if strings.TrimSpace(stringValue(input["style_profile"])) != "" || len(stringArrayValue(input["expected_visual_contract"])) > 0 {
		kind = "ui"
	}
	if strings.TrimSpace(stringValue(input["smiles"])) != "" || strings.TrimSpace(stringValue(input["expected_smiles"])) != "" {
		kind = "molecule"
	}
	return map[string]any{
		"specialist": map[string]any{
			"kind":                   kind,
			"style_profile":          stringValue(input["style_profile"]),
			"expected_contract":      stringArrayValue(input["expected_visual_contract"]),
			"expected_text":          stringArrayValue(input["expected_text"]),
			"expected_smiles":        stringValue(input["expected_smiles"]),
			"requires_model_review":  true,
			"requires_recorded_pass": true,
		},
	}
}

func normalizeVisualReviewAssessment(input map[string]any) map[string]any {
	verdict := firstNonEmpty(stringValue(input["verdict"]), "partial")
	confidence := firstNonEmpty(stringValue(input["confidence"]), "medium")
	return map[string]any{
		"verdict":                   verdict,
		"findings":                  anySliceValue(input["findings"]),
		"inspected_images":          stringArrayValue(input["inspected_images"]),
		"confidence":                confidence,
		"next_fixes":                stringArrayValue(input["next_fixes"]),
		"visual_challenge_response": strings.ToUpper(strings.TrimSpace(stringValue(input["visual_challenge_response"]))),
		"visual_verified":           verdict == "pass",
	}
}

func visualReviewDecision(evidenceCount int, passed bool) map[string]any {
	if passed {
		return map[string]any{"status": "passed", "should_continue": false, "next_step": "report_passed", "quality_score": float64(100), "confidence_score": float64(90), "evidence_score": float64(100), "issue_score": float64(0), "reasons": []any{"assessment verdict passed"}, "recommended_actions": []any{}}
	}
	if evidenceCount > 0 {
		return map[string]any{"status": "needs_model_assessment", "should_continue": true, "next_step": "record_assessment", "quality_score": float64(60), "confidence_score": float64(50), "evidence_score": float64(80), "issue_score": float64(20), "reasons": []any{"visual evidence collected but no assessment recorded"}, "recommended_actions": []any{"inspect attached evidence", "call VisualReview record_assessment"}}
	}
	return map[string]any{"status": "needs_evidence", "should_continue": true, "next_step": "collect_evidence", "quality_score": float64(0), "confidence_score": float64(0), "evidence_score": float64(0), "issue_score": float64(100), "reasons": []any{"no visual evidence collected"}, "recommended_actions": []any{"provide image_path or image_paths"}}
}

func visualReviewID(objective string) string {
	raw := fmt.Sprintf("%s\n%s", strings.TrimSpace(objective), time.Now().UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(raw))
	return "visual-review-" + hex.EncodeToString(sum[:])[:16]
}

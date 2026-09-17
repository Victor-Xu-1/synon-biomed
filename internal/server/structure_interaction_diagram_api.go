package server

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software"
)

const (
	maxStructureInteractionInputBytes  = 24 << 20
	maxStructureInteractionOutputBytes = 8 << 20
	maxStructureInteractionReportBytes = 1 << 20
	structureInteractionRuntimeTimeout = 6 * time.Minute
	maxStructureInteractionTimeout     = 10 * time.Minute
)

//go:embed structure_interaction_diagram.py
var structureInteractionDiagramProgram []byte

//go:embed structure_interaction_renderer.py
var structureInteractionDiagramRenderer []byte

//go:embed structure_interaction_pocket_geometry.py
var structureInteractionPocketGeometry []byte

//go:embed structure_interaction_solvent.py
var structureInteractionSolvent []byte

var structureInteractionResidueName = regexp.MustCompile(`^[A-Z0-9]{1,3}$`)

type structureInteractionDiagramRequest struct {
	Content           string `json:"content"`
	Filename          string `json:"filename"`
	LigandResidueName string `json:"ligand_residue_name"`
	Smiles            string `json:"smiles"`
	LigandLabel       string `json:"ligand_label,omitempty"`
	PoseLabel         string `json:"pose_label,omitempty"`
	ReportOnly        bool   `json:"report_only,omitempty"`
}

type structureInteractionDiagramReport struct {
	Contract                 bool    `json:"contract"`
	OK                       bool    `json:"ok"`
	Engine                   string  `json:"engine"`
	EngineRelease            string  `json:"engine_release"`
	InputSHA256              string  `json:"input_sha256"`
	LigandResidueName        string  `json:"ligand_residue_name"`
	LigandLabel              string  `json:"ligand_label"`
	PoseLabel                string  `json:"pose_label"`
	LigandAtomCount          int     `json:"ligand_atom_count"`
	HydrogenBondCount        int     `json:"hydrogen_bond_count"`
	SaltBridgeCount          int     `json:"salt_bridge_count"`
	Width                    int     `json:"width"`
	Height                   int     `json:"height"`
	PNGDPI                   int     `json:"png_dpi"`
	SVG                      bool    `json:"svg"`
	Message                  string  `json:"message,omitempty"`
	ErrorType                string  `json:"error_type,omitempty"`
	ProteinAtomCount         int     `json:"protein_atom_count,omitempty"`
	InteractionCount         int     `json:"interaction_count,omitempty"`
	ResidueCount             int     `json:"residue_count,omitempty"`
	AnalysisEngine           any     `json:"analysis_engine,omitempty"`
	DepictionEngine          any     `json:"depiction_engine,omitempty"`
	Interactions             any     `json:"interactions,omitempty"`
	DetectedInteractionKinds any     `json:"detected_interaction_kinds,omitempty"`
	InteractionKindCounts    any     `json:"interaction_kind_counts,omitempty"`
	PocketResidues           any     `json:"pocket_residues,omitempty"`
	PocketRadiusAngstrom     float64 `json:"pocket_radius_angstrom,omitempty"`
	PocketGeometryMethod     string  `json:"pocket_geometry_method,omitempty"`
	SolventExposure          any     `json:"solvent_exposure,omitempty"`
	SolventOpenings          any     `json:"solvent_openings,omitempty"`
	SolventOpeningMethod     string  `json:"solvent_opening_method,omitempty"`
	PocketOpeningCount       int     `json:"pocket_opening_count,omitempty"`
	Warnings                 any     `json:"warnings,omitempty"`
}

func (s *Server) registerStructureInteractionDiagramRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("POST /api/frames/{frameId}/structure-interaction-diagram", s.handleStructureInteractionDiagram)
}

func (s *Server) handleStructureInteractionDiagram(w http.ResponseWriter, r *http.Request) {
	frameID := strings.TrimSpace(r.PathValue("frameId"))
	access, ok := s.kernelFrameRequestAccess(w, r, frameID, true)
	if !ok {
		return
	}
	request, err := decodeStructureInteractionDiagramRequest(r)
	if err != nil {
		writeStructureInteractionDiagramError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	cacheKey := structureInteractionDiagramCacheKey(access, request)
	result, completed := structureInteractionResults.Do(
		r.Context(),
		cacheKey,
		structureInteractionRequestBytes(request),
		maxStructureInteractionTimeout,
		func(workContext context.Context) structureInteractionHTTPResult {
			capture := newStructureInteractionCaptureWriter()
			s.executeStructureInteractionDiagram(capture, workContext, access, request)
			return capture.Result()
		},
	)
	if !completed {
		return
	}
	writeStructureInteractionHTTPResult(w, result)
}

func structureInteractionRequestBytes(request structureInteractionDiagramRequest) int {
	return len(request.Content) + len(request.Filename) + len(request.LigandResidueName) + len(request.Smiles) +
		len(request.LigandLabel) + len(request.PoseLabel) + 1
}

func newStructureInteractionRuntimeRequest(inputSHA256 string) software.Request {
	return software.Request{
		Capability:  "protein-ligand-interaction-diagram",
		Provider:    software.LocalProviderID,
		Language:    "python",
		InputSHA256: strings.TrimSpace(inputSHA256),
		Packages: []software.PackageRequirement{
			{Manager: software.PackageManagerPip, Spec: "prolif==2.2.1"},
			// Match the service-owned managed Python baseline. Reusing this pinned
			// distribution avoids downloading a second large RDKit wheel into
			// every otherwise identical interaction environment.
			{Manager: software.PackageManagerPip, Spec: "rdkit==2024.3.5"},
			{Manager: software.PackageManagerPip, Spec: "cairosvg==2.8.2"},
			{Manager: software.PackageManagerPip, Spec: "mdanalysis==2.10.0"},
			{Manager: software.PackageManagerPip, Spec: "pillow==12.3.0"},
			{Manager: software.PackageManagerPip, Spec: "numpy==2.4.6"},
		},
		Imports:    []string{"prolif", "rdkit", "cairosvg", "MDAnalysis", "PIL", "numpy"},
		Executable: "python",
		// A cold governed environment installs the pinned scientific stack
		// before execution. Keep that bounded, but do not cancel a valid first
		// use halfway through the RDKit/MDAnalysis installation.
		TimeoutSeconds: int64(structureInteractionRuntimeTimeout / time.Second),
	}
}

func (s *Server) executeStructureInteractionDiagram(
	w http.ResponseWriter,
	parentContext context.Context,
	access workspace.KernelFrameAccess,
	request structureInteractionDiagramRequest,
) {
	inputDigest := sha256.Sum256([]byte(request.Content))
	runtimeRequest := newStructureInteractionRuntimeRequest(hex.EncodeToString(inputDigest[:]))
	runtimeRequest.Arguments = []string{
		"structure_interaction_diagram.py",
		"--input", "interaction-complex.pdb",
		"--svg", "interaction-diagram.svg",
		"--png", "interaction-diagram.png",
		"--report", "interaction-report.json",
		"--ligand-residue-name", request.LigandResidueName,
		"--smiles", request.Smiles,
		"--ligand-label", request.LigandLabel,
		"--pose-label", request.PoseLabel,
	}
	runtimeRequest.ExpectedOutputs = []software.OutputWitness{
		{Path: "interaction-report.json", MinBytes: 2, Format: "json", RequiredJSONTrue: []string{"/contract"}},
	}
	if request.ReportOnly {
		runtimeRequest.Arguments = append(runtimeRequest.Arguments, "--report-only")
	} else {
		runtimeRequest.ExpectedOutputs = append(runtimeRequest.ExpectedOutputs,
			software.OutputWitness{Path: "interaction-diagram.svg", MinBytes: 100, Format: "text"},
			software.OutputWitness{Path: "interaction-diagram.png", MinBytes: 8},
		)
	}
	rawCanonicalRequest, err := software.CanonicalRequestJSON(runtimeRequest)
	if err != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "runtime_request_invalid", "the Synon interaction runtime request could not be admitted", false)
		return
	}
	var runtimeInput map[string]any
	if err := json.Unmarshal(rawCanonicalRequest, &runtimeInput); err != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "runtime_request_invalid", "the Synon interaction runtime request could not be encoded", false)
		return
	}
	ctx, cancel := context.WithTimeout(parentContext, maxStructureInteractionTimeout)
	defer cancel()
	controller, plan, err := s.resolveSoftwareRuntime(ctx, runtimeInput)
	if err != nil {
		writeStructureInteractionDiagramError(w, http.StatusServiceUnavailable, "software_runtime_unavailable", "the unified software runtime is not ready for interaction diagrams", true)
		return
	}
	operationID := uuid.NewString()
	provision, harness, err := s.provisionSoftwareRuntime(ctx, controller, plan, operationID)
	if err != nil {
		writeStructureInteractionSoftwareError(w, http.StatusServiceUnavailable, err)
		return
	}

	operationDir, mounts, protectedPaths, err := s.prepareStructureMinimizationWorkspace(access)
	if err != nil {
		writeStructureInteractionDiagramError(w, http.StatusServiceUnavailable, "workspace_unavailable", "the task-scoped interaction workspace could not be prepared", true)
		return
	}
	defer os.RemoveAll(operationDir)
	inputPath := filepath.Join(operationDir, "interaction-complex.pdb")
	scriptPath := filepath.Join(operationDir, "structure_interaction_diagram.py")
	rendererPath := filepath.Join(operationDir, "structure_interaction_renderer.py")
	geometryPath := filepath.Join(operationDir, "structure_interaction_pocket_geometry.py")
	solventPath := filepath.Join(operationDir, "structure_interaction_solvent.py")
	if err := os.WriteFile(inputPath, []byte(request.Content), 0o600); err != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "input_stage_failed", "the structure input could not be staged", false)
		return
	}
	if err := os.WriteFile(scriptPath, structureInteractionDiagramProgram, 0o600); err != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "runtime_stage_failed", "the Synon interaction engine could not be staged", false)
		return
	}
	if err := os.WriteFile(rendererPath, structureInteractionDiagramRenderer, 0o600); err != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "runtime_stage_failed", "the Synon interaction renderer could not be staged", false)
		return
	}

	if err := os.WriteFile(geometryPath, structureInteractionPocketGeometry, 0o600); err != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "runtime_stage_failed", "the interaction pocket geometry module could not be staged", false)
		return
	}
	if err := os.WriteFile(solventPath, structureInteractionSolvent, 0o600); err != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "runtime_stage_failed", "the interaction solvent module could not be staged", false)
		return
	}

	kernelID := softwareRuntimeKernelID(operationID)
	worker, err := s.kernelManager.StartSession(kernelruntime.SessionSpec{
		KernelID:               kernelID,
		OwnerID:                access.UserID,
		ProjectID:              access.Frame.ProjectID,
		FrameID:                access.Frame.ID,
		FrameIncarnationID:     access.Frame.IncarnationID,
		RootFrameID:            access.Frame.RootFrameID,
		RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName:              access.Frame.AgentName,
		DelegateName:           access.DelegateName,
		KernelKind:             "analysis",
		Language:               "python",
		Environment:            plan.Environment,
		RuntimeGeneration:      provision.Generation,
		WorkspaceDir:           operationDir,
		Mounts:                 mounts,
		ProtectedPaths:         protectedPaths,
	})
	if err != nil {
		writeStructureInteractionDiagramError(w, http.StatusServiceUnavailable, "kernel_start_failed", "the task-owned interaction kernel could not start", true)
		return
	}
	executionContext, cancelExecution := context.WithTimeout(ctx, time.Duration(plan.Request.TimeoutSeconds)*time.Second+softwareRuntimeExecutionTimeoutGrace)
	response, executeErr := worker.Execute(executionContext, harness, "agent")
	cancelExecution()
	closeErr := s.closeAgentKernel(kernelID)
	if closeErr != nil && executeErr == nil && response.Error == "" {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "kernel_close_failed", "the task-owned interaction kernel could not be closed safely", true)
		return
	}
	if executeErr != nil && ctx.Err() != nil {
		writeStructureInteractionDiagramError(w, http.StatusGatewayTimeout, "interaction_diagram_timeout", "Synon 2D interaction generation exceeded the bounded execution time", true)
		return
	}
	exitStatus := "ok"
	if executeErr != nil || response.Error != "" || response.Interrupted {
		exitStatus = "error"
	}
	visible, visibleErr := softwareRuntimeVisibleResult(rawCanonicalRequest, plan.Environment, provision.Generation, harness, map[string]any{
		"stdout": response.Stdout, "stderr": response.Stderr, "error": response.Error,
		"exit_status": exitStatus, "exec_id": response.ID, "kernel_id": kernelID, "cell_index": nil,
	})
	if visibleErr != nil {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "runtime_receipt_invalid", "the unified interaction runtime returned an invalid execution receipt", false)
		return
	}
	report, reportErr := readStructureInteractionDiagramReport(operationDir)
	if !boolValue(visible["ok"], false) {
		message := "Synon could not generate a ligand interaction diagram for the selected pose"
		if reportErr == nil && safeStructureInteractionMessage(report.Message) != "" {
			message = safeStructureInteractionMessage(report.Message)
		}
		writeStructureInteractionDiagramError(w, http.StatusUnprocessableEntity, "interaction_diagram_failed", message, false)
		return
	}
	if reportErr != nil || !report.Contract || !report.OK || report.Engine != "Synon 2D Interaction Engine" {
		writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "interaction_report_invalid", "the Synon interaction report failed validation", false)
		return
	}
	result := map[string]any{
		"ok": true, "status": "completed", "report": report,
		"runtime": structureMinimizationRuntimeSummary(visible),
	}
	if !request.ReportOnly {
		svg, err := readBoundedStructureFile(filepath.Join(operationDir, "interaction-diagram.svg"), maxStructureInteractionOutputBytes)
		if err != nil || !utf8.Valid(svg) || !strings.Contains(string(svg), "<svg") {
			writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "interaction_svg_invalid", "the Synon vector diagram failed validation", false)
			return
		}
		png, err := readBoundedStructureFile(filepath.Join(operationDir, "interaction-diagram.png"), maxStructureInteractionOutputBytes)
		if err != nil || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
			writeStructureInteractionDiagramError(w, http.StatusInternalServerError, "interaction_png_invalid", "the Synon publication image failed validation", false)
			return
		}
		result["svg"] = string(svg)
		result["png_base64"] = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, http.StatusOK, result)
}

func structureInteractionDiagramCacheKey(
	access workspace.KernelFrameAccess,
	request structureInteractionDiagramRequest,
) string {
	digest := sha256.New()
	for _, part := range []string{
		access.UserID,
		access.Frame.ProjectID,
		access.Frame.ID,
		access.Frame.IncarnationID,
		access.RootFrameIncarnationID,
		request.Content,
		request.Filename,
		request.LigandResidueName,
		request.Smiles,
		request.LigandLabel,
		request.PoseLabel,
	} {
		writeStructureInteractionCacheKeyPart(digest, part)
	}
	if request.ReportOnly {
		_, _ = digest.Write([]byte{1})
	} else {
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeStructureInteractionCacheKeyPart(destination io.Writer, value string) {
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = io.WriteString(destination, value)
}

func decodeStructureInteractionDiagramRequest(r *http.Request) (structureInteractionDiagramRequest, error) {
	var request structureInteractionDiagramRequest
	if r == nil || r.Body == nil {
		return request, errors.New("request body is required")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxStructureInteractionInputBytes+32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("request body is invalid")
	}
	request.Filename = strings.TrimSpace(request.Filename)
	request.LigandResidueName = strings.ToUpper(strings.TrimSpace(request.LigandResidueName))
	request.Smiles = strings.TrimSpace(request.Smiles)
	request.LigandLabel = strings.TrimSpace(request.LigandLabel)
	request.PoseLabel = strings.TrimSpace(request.PoseLabel)
	if len(request.Content) == 0 || len(request.Content) > maxStructureInteractionInputBytes || !utf8.ValidString(request.Content) {
		return request, errors.New("structure content is missing or exceeds the bounded size limit")
	}
	if extension := strings.ToLower(filepath.Ext(request.Filename)); extension != ".pdb" && extension != ".ent" {
		return request, errors.New("publication interaction diagrams currently require a PDB complex")
	}
	if !structureInteractionResidueName.MatchString(request.LigandResidueName) {
		return request, errors.New("ligand residue name is invalid")
	}
	if len(request.Smiles) == 0 || len(request.Smiles) > 8192 || strings.ContainsAny(request.Smiles, "\r\n\x00") {
		return request, errors.New("candidate SMILES is missing or invalid")
	}
	if len(request.LigandLabel) > 128 || len(request.PoseLabel) > 256 || strings.ContainsAny(request.LigandLabel+request.PoseLabel, "\x00\r\n") {
		return request, errors.New("interaction diagram labels are invalid")
	}
	return request, nil
}

func readStructureInteractionDiagramReport(operationDir string) (structureInteractionDiagramReport, error) {
	var report structureInteractionDiagramReport
	content, err := readBoundedStructureFile(filepath.Join(operationDir, "interaction-report.json"), maxStructureInteractionReportBytes)
	if err != nil || !utf8.Valid(content) {
		return report, errors.New("interaction report is unavailable")
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, errors.New("interaction report is invalid")
	}
	return report, nil
}

func safeStructureInteractionMessage(message string) string {
	switch strings.TrimSpace(message) {
	case "structure input is missing or exceeds the bounded size limit",
		"structure contains too many atoms",
		"protein atoms are unavailable in the complex",
		"the selected ligand residue is unavailable in the complex",
		"selected ligand exceeds the bounded atom limit",
		"candidate SMILES could not be parsed",
		"ProLIF found no supported interactions for the selected pose":
		return strings.TrimSpace(message)
	default:
		if strings.HasPrefix(strings.TrimSpace(message), "candidate SMILES has ") {
			return strings.TrimSpace(message)
		}
		return ""
	}
}

func writeStructureInteractionDiagramError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	writeJSON(w, status, map[string]any{
		"ok": false, "status": "failed", "code": code, "message": message, "retryable": retryable,
	})
}

func writeStructureInteractionSoftwareError(w http.ResponseWriter, status int, err error) {
	code := "software_runtime_provision_failed"
	recovery := "inspect_the_governed_runtime_diagnostic_then_retry_the_same_provider_plan"
	repairScope := "same_provider_plan"
	var operationErr *software.OperationError
	if errors.As(err, &operationErr) && operationErr != nil {
		if value := strings.TrimSpace(operationErr.Code); value != "" {
			code = value
		}
		if value := strings.TrimSpace(operationErr.Recovery); value != "" {
			recovery = value
		}
		if value := strings.TrimSpace(operationErr.RepairScope); value != "" {
			repairScope = value
		}
	}
	message := map[string]string{
		"software_install_timeout":            "interaction runtime preparation exceeded its bounded timeout",
		"software_install_failed":             "interaction runtime preparation failed",
		"software_repair_failed":              "interaction runtime repair failed",
		"software_dependency_unavailable":     "a declared interaction dependency is unavailable in the governed provider plan",
		"software_environment_witness_failed": "the interaction environment witness did not produce a valid result",
		"software_request_unsupported":        "no registered software provider satisfies the interaction runtime request",
		"software_import_missing":             "a required interaction-engine import is unavailable",
	}[code]
	if message == "" {
		message = "the verified interaction runtime could not be provisioned"
	}
	writeJSON(w, status, map[string]any{
		"ok": false, "status": "failed", "code": code, "message": message,
		"recovery": recovery, "repair_scope": repairScope, "retryable": true,
	})
}

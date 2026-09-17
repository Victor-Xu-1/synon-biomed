package server

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"synon-go/internal/failurecontract"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software"
)

const (
	maxStructureMinimizationInputBytes  = 16 << 20
	maxStructureMinimizationReportBytes = 1 << 20
	maxStructureMinimizationTimeout     = 6 * time.Minute
)

//go:embed structure_minimization.py
var structureMinimizationProgram []byte

type structureMinimizationRequest struct {
	Content            string  `json:"content"`
	Filename           string  `json:"filename"`
	ForceField         string  `json:"force_field,omitempty"`
	Scope              string  `json:"scope,omitempty"`
	ProteinEnvironment string  `json:"protein_environment,omitempty"`
	LigandResidueName  string  `json:"ligand_residue_name,omitempty"`
	MaxIterations      int     `json:"max_iterations,omitempty"`
	Tolerance          float64 `json:"tolerance,omitempty"`
}

type structureMinimizationFormat struct {
	input      string
	output     string
	inputName  string
	outputName string
}

func (s *Server) registerStructureMinimizationRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("POST /api/frames/{frameId}/structure-minimization", s.handleStructureMinimization)
}

func (s *Server) handleStructureMinimization(w http.ResponseWriter, r *http.Request) {
	frameID := strings.TrimSpace(r.PathValue("frameId"))
	access, ok := s.kernelFrameRequestAccess(w, r, frameID, true)
	if !ok {
		return
	}

	request, err := decodeStructureMinimizationRequest(r)
	if err != nil {
		writeStructureMinimizationError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	format, err := normalizeStructureMinimizationFormat(request.Filename)
	if err != nil {
		writeStructureMinimizationError(w, http.StatusUnprocessableEntity, "unsupported_structure_format", err.Error(), false)
		return
	}

	sourceEnvironment := ""
	if s.kernelManager != nil {
		sourceEnvironment = strings.TrimSpace(s.kernelManager.ManagedPythonEnvironmentName())
	}
	if sourceEnvironment == "" {
		writeStructureMinimizationError(w, http.StatusServiceUnavailable, "managed_python_unavailable", "the verified managed Python environment is not available", true)
		return
	}

	inputDigest := sha256.Sum256([]byte(request.Content))
	inputSHA256 := hex.EncodeToString(inputDigest[:])
	runtimeRequest := software.Request{
		Capability:        "structure-energy-minimization",
		Provider:          software.LocalProviderID,
		Language:          "python",
		SourceEnvironment: sourceEnvironment,
		InputSHA256:       inputSHA256,
		Imports:           []string{"rdkit"},
		Executable:        "python",
		Arguments: []string{
			"_structure_minimize.py",
			"--input", format.inputName,
			"--output", format.outputName,
			"--report", "minimization-report.json",
			"--format", format.input,
			"--force-field", request.ForceField,
			"--scope", request.Scope,
			"--protein-environment", request.ProteinEnvironment,
			"--ligand-residue-name", request.LigandResidueName,
			"--max-iterations", strconv.Itoa(request.MaxIterations),
			"--tolerance", strconv.FormatFloat(request.Tolerance, 'g', -1, 64),
		},
		ExpectedOutputs: []software.OutputWitness{
			{Path: format.outputName, MinBytes: 1, Format: "text"},
			{Path: "minimization-report.json", MinBytes: 2, Format: "json", RequiredJSONTrue: []string{"/contract"}},
		},
		TimeoutSeconds: 5 * 60,
	}
	rawCanonicalRequest, err := software.CanonicalRequestJSON(runtimeRequest)
	if err != nil {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "runtime_request_invalid", "the structure minimization runtime request could not be admitted", false)
		return
	}
	var runtimeInput map[string]any
	if err := json.Unmarshal(rawCanonicalRequest, &runtimeInput); err != nil {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "runtime_request_invalid", "the structure minimization runtime request could not be encoded", false)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), maxStructureMinimizationTimeout)
	defer cancel()
	controller, plan, err := s.resolveSoftwareRuntime(ctx, runtimeInput)
	if err != nil {
		writeStructureMinimizationError(w, http.StatusServiceUnavailable, "software_runtime_unavailable", "the unified software runtime is not ready for structure minimization", true)
		return
	}
	operationID := uuid.NewString()
	provision, harness, err := s.provisionSoftwareRuntime(ctx, controller, plan, operationID)
	if err != nil {
		writeStructureMinimizationSoftwareError(w, http.StatusServiceUnavailable, err)
		return
	}

	operationDir, mounts, protectedPaths, err := s.prepareStructureMinimizationWorkspace(access)
	if err != nil {
		writeStructureMinimizationError(w, http.StatusServiceUnavailable, "workspace_unavailable", "the task-scoped structure minimization workspace could not be prepared", true)
		return
	}
	defer os.RemoveAll(operationDir)

	inputPath := filepath.Join(operationDir, format.inputName)
	scriptPath := filepath.Join(operationDir, "_structure_minimize.py")
	if err := os.WriteFile(inputPath, []byte(request.Content), 0o600); err != nil {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "input_stage_failed", "the structure input could not be staged", false)
		return
	}
	if err := os.WriteFile(scriptPath, structureMinimizationProgram, 0o600); err != nil {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "runtime_stage_failed", "the structure minimization runtime could not be staged", false)
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
		writeStructureMinimizationError(w, http.StatusServiceUnavailable, "kernel_start_failed", "the task-owned structure minimization kernel could not start", true)
		return
	}

	executionContext, cancelExecution := context.WithTimeout(ctx, time.Duration(plan.Request.TimeoutSeconds)*time.Second+softwareRuntimeExecutionTimeoutGrace)
	response, executeErr := worker.Execute(executionContext, harness, "agent")
	cancelExecution()
	closeErr := s.closeAgentKernel(kernelID)
	if closeErr != nil && executeErr == nil && response.Error == "" {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "kernel_close_failed", "the task-owned structure minimization kernel could not be closed safely", true)
		return
	}
	if executeErr != nil && ctx.Err() != nil {
		writeStructureMinimizationError(w, http.StatusGatewayTimeout, "structure_minimization_timeout", "structure minimization exceeded its bounded execution time", true)
		return
	}

	exitStatus := "ok"
	if executeErr != nil || response.Error != "" || response.Interrupted {
		exitStatus = "error"
	}
	visible, err := softwareRuntimeVisibleResult(rawCanonicalRequest, plan.Environment, provision.Generation, harness, map[string]any{
		"stdout":      response.Stdout,
		"stderr":      response.Stderr,
		"error":       response.Error,
		"exit_status": exitStatus,
		"exec_id":     response.ID,
		"kernel_id":   kernelID,
		"cell_index":  nil,
	})
	if err != nil {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "runtime_receipt_invalid", "the unified software runtime returned an invalid execution receipt", false)
		return
	}
	if !boolValue(visible["ok"], false) {
		message := "force-field minimization did not complete; validate the input structure and retry"
		if report, reportErr := readStructureMinimizationReport(operationDir); reportErr == nil {
			message = structureMinimizationFailureMessage(report)
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"ok": false, "status": "failed", "code": stringValue(visible["code"]),
			"message": message, "retryable": boolValue(visible["retryable"], false),
			"runtime": structureMinimizationRuntimeSummary(visible),
		})
		return
	}

	report, err := readStructureMinimizationReport(operationDir)
	if err != nil || !boolValue(report["contract"], false) || !boolValue(report["ok"], false) {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "minimization_report_invalid", "the structure minimization report failed its contract validation", false)
		return
	}
	outputBytes, err := readBoundedStructureFile(filepath.Join(operationDir, format.outputName), maxStructureMinimizationInputBytes)
	if err != nil || !utf8.Valid(outputBytes) {
		writeStructureMinimizationError(w, http.StatusInternalServerError, "minimization_output_invalid", "the minimized structure output failed its text validation", false)
		return
	}

	responseBody := map[string]any{
		"ok":          true,
		"status":      "completed",
		"content":     string(outputBytes),
		"filename":    format.outputName,
		"format":      format.output,
		"force_field": request.ForceField,
		"runtime":     structureMinimizationRuntimeSummary(visible),
	}
	for _, key := range []string{
		"atom_count", "molecule_count", "generated_conformer", "before_energy",
		"after_energy", "converged", "message", "scope", "protein_environment",
		"ligand_atom_count", "fixed_atom_count", "protein_present", "ligand_residue",
	} {
		if value, found := report[key]; found {
			responseBody[key] = value
		}
	}
	writeJSON(w, http.StatusOK, responseBody)
}

func decodeStructureMinimizationRequest(r *http.Request) (structureMinimizationRequest, error) {
	if r == nil || r.Body == nil {
		return structureMinimizationRequest{}, errors.New("request body is required")
	}
	if r.ContentLength > maxStructureMinimizationInputBytes {
		return structureMinimizationRequest{}, errors.New("request body exceeds the bounded limit")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxStructureMinimizationInputBytes+1))
	decoder.DisallowUnknownFields()
	var request structureMinimizationRequest
	if err := decoder.Decode(&request); err != nil {
		return structureMinimizationRequest{}, errors.New("request body is not valid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return structureMinimizationRequest{}, errors.New("request body contains trailing data")
	}
	if !utf8.ValidString(request.Content) || strings.ContainsRune(request.Content, '\x00') || len(request.Content) == 0 || len(request.Content) > maxStructureMinimizationInputBytes {
		return structureMinimizationRequest{}, errors.New("content must be bounded, non-empty UTF-8 text")
	}
	if len(request.Filename) == 0 || len(request.Filename) > 256 {
		return structureMinimizationRequest{}, errors.New("filename is invalid")
	}
	request.ForceField = strings.ToLower(strings.TrimSpace(request.ForceField))
	if request.ForceField == "" {
		request.ForceField = "uff"
	}
	switch request.ForceField {
	case "uff", "mmff94", "mmff94s":
	default:
		return structureMinimizationRequest{}, errors.New("force_field must be uff, mmff94, or mmff94s")
	}
	request.Scope = strings.ToLower(strings.TrimSpace(request.Scope))
	if request.Scope == "" {
		request.Scope = "ligand"
	}
	if request.Scope != "ligand" {
		return structureMinimizationRequest{}, errors.New("scope must be ligand")
	}
	request.ProteinEnvironment = strings.ToLower(strings.TrimSpace(request.ProteinEnvironment))
	if request.ProteinEnvironment == "" {
		request.ProteinEnvironment = "fixed"
	}
	if request.ProteinEnvironment != "fixed" {
		return structureMinimizationRequest{}, errors.New("protein_environment must be fixed")
	}
	request.LigandResidueName = strings.ToUpper(strings.TrimSpace(request.LigandResidueName))
	if len(request.LigandResidueName) > 3 {
		return structureMinimizationRequest{}, errors.New("ligand_residue_name must be a PDB residue name")
	}
	for _, character := range request.LigandResidueName {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return structureMinimizationRequest{}, errors.New("ligand_residue_name must be a PDB residue name")
		}
	}
	if request.MaxIterations == 0 {
		request.MaxIterations = 200
	}
	if request.MaxIterations < 1 || request.MaxIterations > 1000 {
		return structureMinimizationRequest{}, errors.New("max_iterations is outside the bounded range")
	}
	if request.Tolerance == 0 {
		request.Tolerance = 1e-4
	}
	if math.IsNaN(request.Tolerance) || math.IsInf(request.Tolerance, 0) || request.Tolerance <= 0 || request.Tolerance > 1 {
		return structureMinimizationRequest{}, errors.New("tolerance is outside the bounded range")
	}
	return request, nil
}

func normalizeStructureMinimizationFormat(filename string) (structureMinimizationFormat, error) {
	if strings.ContainsAny(filename, "/\\") || filepath.Base(filename) != filename {
		return structureMinimizationFormat{}, errors.New("filename must be a single structure filename")
	}
	extension := strings.ToLower(filepath.Ext(filename))
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if base == "" || len(base) > 160 {
		return structureMinimizationFormat{}, errors.New("filename base is invalid")
	}
	result := structureMinimizationFormat{inputName: filename}
	switch extension {
	case ".sdf", ".sd":
		result.input = "sdf"
		result.output = "sdf"
	case ".mol":
		result.input = "mol"
		result.output = "sdf"
	case ".mol2":
		result.input = "mol2"
		result.output = "sdf"
	case ".pdb", ".ent":
		result.input = "pdb"
		result.output = "pdb"
	default:
		return structureMinimizationFormat{}, errors.New("supported structure formats are SDF, MOL, MOL2, and PDB")
	}
	result.outputName = base + "-minimized." + result.output
	return result, nil
}

func (s *Server) prepareStructureMinimizationWorkspace(access workspace.KernelFrameAccess) (string, []kernelruntime.WorkerMount, []string, error) {
	if s == nil || strings.TrimSpace(access.Frame.ProjectID) == "" {
		return "", nil, nil, errors.New("structure minimization workspace identity is unavailable")
	}
	baseWorkspace, err := s.defaultAgentKernelWorkspace(access.Frame.ProjectID)
	if err != nil {
		return "", nil, nil, err
	}
	identity := &agentKernelContext{access: access, workspaceDir: baseWorkspace}
	s.hostGrantKernelMu.Lock()
	defer s.hostGrantKernelMu.Unlock()
	if s.hostGrantKernelFences[access.UserID] {
		return "", nil, nil, errors.New("kernel host access is fenced")
	}
	if err := s.ensureAgentKernelManagedDirectories(); err != nil {
		return "", nil, nil, err
	}
	workspaceRoot, err := s.ensureAgentWorkspaceRoot(identity)
	if err != nil {
		return "", nil, nil, err
	}
	operationDir, err := os.MkdirTemp(workspaceRoot, ".structure-minimize-*")
	if err != nil {
		return "", nil, nil, err
	}
	protectedPaths, err := s.agentKernelProtectedPaths()
	if err != nil {
		os.RemoveAll(operationDir)
		return "", nil, nil, err
	}
	mounts, err := s.agentKernelConfinementMounts(access.UserID, operationDir, protectedPaths)
	if err != nil {
		os.RemoveAll(operationDir)
		return "", nil, nil, err
	}
	return operationDir, mounts, protectedPaths, nil
}

func readStructureMinimizationReport(operationDir string) (map[string]any, error) {
	content, err := readBoundedStructureFile(filepath.Join(operationDir, "minimization-report.json"), maxStructureMinimizationReportBytes)
	if err != nil || !utf8.Valid(content) {
		return nil, errors.New("minimization report is unavailable")
	}
	var report map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return nil, errors.New("minimization report is not valid JSON")
	}
	return report, nil
}

func readBoundedStructureFile(path string, maxBytes int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil || len(content) > maxBytes {
		return nil, errors.New("structure file exceeds the bounded limit")
	}
	return content, nil
}

func structureMinimizationRuntimeSummary(visible map[string]any) map[string]any {
	return map[string]any{
		"provider_id":       visible["provider_id"],
		"environment":       visible["environment"],
		"generation":        visible["runtime_generation"],
		"request_digest":    visible["request_digest"],
		"exec_id":           visible["exec_id"],
		"kernel_id":         visible["kernel_id"],
		"preflight_checked": visible["preflight_checked"],
	}
}

func structureMinimizationFailureMessage(report map[string]any) string {
	const fallback = "force-field minimization did not complete; validate the input structure and retry"

	message := strings.TrimSpace(stringValue(report["message"]))
	switch message {
	case "input contains no molecules",
		"input contains no ligand",
		"input contains multiple molecules; open one ligand before minimizing",
		"specified ligand residue is not present in the current pose",
		"the protein complex contains multiple ligand candidates; open one ligand pose before minimizing",
		"a conformer could not be generated for the input molecule",
		"a 3D conformer could not be generated for the input molecule",
		"force field could not be constructed",
		"UFF parameters are unavailable for one or more atoms",
		"MMFF94 parameters are unavailable for one or more atoms",
		"MMFF94s parameters are unavailable for one or more atoms",
		"MMFF94 properties are unavailable",
		"MMFF94s properties are unavailable":
		return message
	default:
		return fallback
	}
}

func writeStructureMinimizationError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	writeJSON(w, status, map[string]any{
		"ok": false, "status": "failed", "code": code, "message": message, "retryable": retryable,
	})
}

func writeStructureMinimizationSoftwareError(w http.ResponseWriter, status int, err error) {
	const fallbackMessage = "the verified managed Python environment could not be prepared for structure minimization"
	const fallbackRecovery = "inspect_the_governed_runtime_diagnostic_then_retry_the_same_provider_plan"
	const fallbackRepairScope = "same_provider_plan"

	code := "software_runtime_provision_failed"
	message := fallbackMessage
	recovery := fallbackRecovery
	repairScope := fallbackRepairScope
	var operationErr *software.OperationError
	if errors.As(err, &operationErr) && operationErr != nil {
		if value := strings.TrimSpace(operationErr.Code); value != "" {
			code = value
		}
		// Provider causes can contain local paths, command lines, or package
		// metadata. The HTTP contract exposes the stable diagnosis, not those
		// implementation details.
		message = structureMinimizationSoftwareMessage(code)
		if value := strings.TrimSpace(operationErr.Recovery); value != "" {
			recovery = value
		}
		if value := strings.TrimSpace(operationErr.RepairScope); value != "" {
			repairScope = value
		}
	}
	value := map[string]any{
		"ok":           false,
		"status":       "failed",
		"code":         code,
		"message":      message,
		"recovery":     recovery,
		"repair_scope": repairScope,
	}
	failurecontract.ApplyTerminalJobFailure(value, code)
	writeJSON(w, status, value)
}

func structureMinimizationSoftwareMessage(code string) string {
	if message, ok := map[string]string{
		"managed_python_runtime_unavailable":    "the verified managed Python environment is unavailable",
		"managed_python_generation_unavailable": "the verified managed Python generation is unavailable",
		"software_executable_missing":           "the verified managed Python executable is unavailable",
		"software_import_missing":               "the required structure-minimization Python import is unavailable",
		"software_install_timeout":              "software runtime preparation exceeded its bounded timeout",
		"software_install_failed":               "software runtime preparation failed",
		"software_repair_failed":                "software runtime repair failed",
		"software_dependency_unavailable":       "the declared dependency is unavailable in the governed provider plan",
		"software_environment_witness_failed":   "the managed environment witness did not produce a valid result",
		"software_request_unsupported":          "no registered software provider satisfies the declared request",
		"software_api_contract_mismatch":        "the runtime invocation does not match the documented software API contract",
		"software_cli_arguments_invalid":        "the documented executable rejected the supplied arguments",
		"software_runtime_import_missing":       "the runtime invocation imported a module absent from the admitted environment",
		"software_output_validation_failed":     "the declared output witness failed",
		"software_quality_contract_failed":      "the declared scientific quality contract failed",
		"software_input_contract_failed":        "the scientific input does not satisfy the selected engine contract",
		"execution_path_exhausted":              "the documented execution path failed twice and is closed",
	}[code]; ok {
		return message
	}
	return "the verified managed Python environment could not be prepared for structure minimization"
}

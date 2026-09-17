package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software"
)

const (
	structureElectrostaticInputDigestDomain = "synon-biomed:structure-electrostatic-input:v1"
	maxStructureElectrostaticInputBytes     = 24 << 20
	maxStructureElectrostaticLigandBytes    = 4 << 20
	maxStructureElectrostaticLigandCount    = 8
	maxStructureElectrostaticBatchBytes     = 8 << 20
	maxStructureElectrostaticDXBytes        = 64 << 20
	maxStructureElectrostaticTotalDXBytes   = 128 << 20
	maxStructureElectrostaticGridValues     = 6_000_000
	maxStructureElectrostaticTotalGrids     = 24_000_000
	maxStructureElectrostaticProteinAtoms   = 200_000
	maxStructureElectrostaticPerLigand      = 2_048
	maxStructureElectrostaticLigandAtoms    = 8_192
	maxStructureElectrostaticReportBytes    = 1 << 20
	maxStructureElectrostaticEncodedBytes   = 24 << 20
	maxStructureElectrostaticTotalEncoded   = 36 << 20
	structureElectrostaticRuntimeTimeout    = 12 * time.Minute
	maxStructureElectrostaticTimeout        = 16 * time.Minute
)

var structureElectrostaticLigandKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

//go:embed structure_electrostatic_map.py
var structureElectrostaticMapProgram []byte

var structureElectrostaticResults = newStructureInteractionResultCoordinator(
	12,
	96<<20,
	30*time.Minute,
	1,
	2,
	48<<20,
)

type structureElectrostaticMapRequest struct {
	Content                string                              `json:"content"`
	Filename               string                              `json:"filename"`
	LigandMolBlock         string                              `json:"ligand_mol_block,omitempty"`
	LigandMolBlocks        []structureElectrostaticLigandInput `json:"ligand_mol_blocks,omitempty"`
	PH                     float64                             `json:"ph"`
	IonicStrengthMolar     float64                             `json:"ionic_strength_molar"`
	ligandMolBlockPresent  bool
	ligandMolBlocksPresent bool
}

type structureElectrostaticLigandInput struct {
	Key      string `json:"key"`
	MolBlock string `json:"mol_block"`
}

type structureElectrostaticMapRequestPayload struct {
	Content            string                               `json:"content"`
	Filename           string                               `json:"filename"`
	LigandMolBlock     *string                              `json:"ligand_mol_block"`
	LigandMolBlocks    *[]structureElectrostaticLigandInput `json:"ligand_mol_blocks"`
	PH                 float64                              `json:"ph"`
	IonicStrengthMolar float64                              `json:"ionic_strength_molar"`
}

type structureElectrostaticGridReport struct {
	Counts     []int     `json:"counts"`
	Origin     []float64 `json:"origin"`
	Delta      []float64 `json:"delta"`
	ValueCount int       `json:"value_count"`
	Minimum    float64   `json:"minimum"`
	Maximum    float64   `json:"maximum"`
}

type structureElectrostaticGridReports struct {
	Protein *structureElectrostaticGridReport           `json:"protein,omitempty"`
	Ligand  *structureElectrostaticGridReport           `json:"ligand,omitempty"`
	Ligands map[string]structureElectrostaticGridReport `json:"ligands,omitempty"`
}

type structureElectrostaticMapReport struct {
	Contract                 bool                              `json:"contract"`
	ContractVersion          string                            `json:"contract_version"`
	OK                       bool                              `json:"ok"`
	Engine                   string                            `json:"engine"`
	CalculationMode          string                            `json:"calculation_mode"`
	GridAlignment            string                            `json:"grid_alignment"`
	EngineVersion            string                            `json:"engine_version"`
	PreparationEngine        string                            `json:"preparation_engine"`
	PreparationEngineVersion string                            `json:"preparation_engine_version"`
	ProteinChargeMethod      string                            `json:"protein_charge_method"`
	LigandChargeMethod       string                            `json:"ligand_charge_method"`
	ForceField               string                            `json:"force_field"`
	PH                       float64                           `json:"ph"`
	IonicStrengthMolar       float64                           `json:"ionic_strength_molar"`
	PotentialUnit            string                            `json:"potential_unit"`
	ColorRange               []float64                         `json:"color_range"`
	InputSHA256              string                            `json:"input_sha256"`
	ProteinAtomCount         int                               `json:"protein_atom_count"`
	LigandAtomCount          int                               `json:"ligand_atom_count"`
	TotalAtomCount           int                               `json:"total_atom_count"`
	MeshSpacingAngstrom      float64                           `json:"mesh_spacing_angstrom"`
	Grids                    structureElectrostaticGridReports `json:"grids"`
	LigandAtomCounts         map[string]int                    `json:"ligand_atom_counts,omitempty"`
	Warnings                 []string                          `json:"warnings"`
	Message                  string                            `json:"message,omitempty"`
	ErrorType                string                            `json:"error_type,omitempty"`
}

func (s *Server) registerStructureElectrostaticMapRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("POST /api/frames/{frameId}/structure-electrostatic-map", s.handleStructureElectrostaticMap)
}

func (s *Server) handleStructureElectrostaticMap(w http.ResponseWriter, r *http.Request) {
	frameID := strings.TrimSpace(r.PathValue("frameId"))
	access, ok := s.kernelFrameRequestAccess(w, r, frameID, true)
	if !ok {
		return
	}
	request, err := decodeStructureElectrostaticMapRequest(r)
	if err != nil {
		writeStructureElectrostaticMapError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	cacheKey := structureElectrostaticMapCacheKey(access, request)
	result, completed := structureElectrostaticResults.Do(
		r.Context(),
		cacheKey,
		structureElectrostaticRequestWeight(request),
		maxStructureElectrostaticTimeout,
		func(workContext context.Context) structureInteractionHTTPResult {
			capture := newStructureInteractionCaptureWriter()
			s.executeStructureElectrostaticMap(capture, workContext, access, request)
			return capture.Result()
		},
	)
	if !completed {
		return
	}
	writeStructureInteractionHTTPResult(w, result)
}

func newStructureElectrostaticRuntimeRequest(inputSHA256 string) software.Request {
	return software.Request{
		Capability:  biomolecularElectrostaticsRuntimeID,
		Provider:    software.LocalProviderID,
		Language:    "python",
		InputSHA256: strings.TrimSpace(inputSHA256),
		Packages: []software.PackageRequirement{
			{Manager: software.PackageManagerConda, Spec: "apbs=3.4.1"},
			{Manager: software.PackageManagerPip, Spec: "pdb2pqr==3.7.1"},
			{Manager: software.PackageManagerPip, Spec: "rdkit==2024.3.5"},
			{Manager: software.PackageManagerPip, Spec: "numpy==2.4.6"},
		},
		Channels:       []string{"conda-forge"},
		Imports:        []string{"pdb2pqr", "rdkit", "numpy"},
		Executable:     "python",
		TimeoutSeconds: int64(structureElectrostaticRuntimeTimeout / time.Second),
	}
}

func decodeStructureElectrostaticMapRequest(r *http.Request) (structureElectrostaticMapRequest, error) {
	request := structureElectrostaticMapRequest{PH: 7.4, IonicStrengthMolar: 0.15}
	if r == nil || r.Body == nil {
		return request, errors.New("request body is required")
	}
	const maxRequestBytes = maxStructureElectrostaticInputBytes + maxStructureElectrostaticBatchBytes + (256 << 10)
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxRequestBytes)+1))
	if err != nil || len(body) > maxRequestBytes {
		return request, errors.New("request body exceeds the bounded size limit")
	}
	payload := structureElectrostaticMapRequestPayload{PH: 7.4, IonicStrengthMolar: 0.15}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return request, errors.New("request body is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, errors.New("request body must contain exactly one JSON value")
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawFields); err != nil {
		return request, errors.New("request body is invalid")
	}
	for _, field := range []string{"ph", "ionic_strength_molar", "ligand_mol_block", "ligand_mol_blocks"} {
		if raw, exists := rawFields[field]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return request, errors.New("request fields cannot be null")
		}
	}
	request.Content = payload.Content
	request.Filename = payload.Filename
	request.PH = payload.PH
	request.IonicStrengthMolar = payload.IonicStrengthMolar
	_, request.ligandMolBlockPresent = rawFields["ligand_mol_block"]
	_, request.ligandMolBlocksPresent = rawFields["ligand_mol_blocks"]
	if payload.LigandMolBlock != nil {
		request.LigandMolBlock = *payload.LigandMolBlock
	}
	if payload.LigandMolBlocks != nil {
		request.LigandMolBlocks = append([]structureElectrostaticLigandInput(nil), (*payload.LigandMolBlocks)...)
	}
	request.Filename = strings.TrimSpace(request.Filename)
	if len(request.Content) == 0 || len(request.Content) > maxStructureElectrostaticInputBytes || !utf8.ValidString(request.Content) || strings.ContainsRune(request.Content, 0) {
		return request, errors.New("structure content is missing or exceeds the bounded size limit")
	}
	if request.ligandMolBlockPresent && request.ligandMolBlocksPresent {
		return request, errors.New("ligand_mol_block and ligand_mol_blocks are mutually exclusive")
	}
	if len(request.LigandMolBlock) > maxStructureElectrostaticLigandBytes || !utf8.ValidString(request.LigandMolBlock) || strings.ContainsRune(request.LigandMolBlock, 0) {
		return request, errors.New("ligand content exceeds the bounded size limit")
	}
	if request.LigandMolBlock != "" && !strings.Contains(request.LigandMolBlock, "M  END") {
		return request, errors.New("ligand content is not a valid mol block")
	}
	if request.ligandMolBlocksPresent {
		if len(request.LigandMolBlocks) == 0 || len(request.LigandMolBlocks) > maxStructureElectrostaticLigandCount {
			return request, errors.New("ligand_mol_blocks must contain a bounded number of ligands")
		}
		totalLigandBytes := 0
		seenKeys := make(map[string]struct{}, len(request.LigandMolBlocks))
		for _, ligand := range request.LigandMolBlocks {
			if ligand.Key != strings.TrimSpace(ligand.Key) || !structureElectrostaticLigandKeyPattern.MatchString(ligand.Key) {
				return request, errors.New("ligand key is invalid")
			}
			normalizedKey := strings.ToUpper(ligand.Key)
			if _, exists := seenKeys[normalizedKey]; exists {
				return request, errors.New("ligand keys must be unique")
			}
			seenKeys[normalizedKey] = struct{}{}
			if len(ligand.MolBlock) == 0 || len(ligand.MolBlock) > maxStructureElectrostaticLigandBytes ||
				!utf8.ValidString(ligand.MolBlock) || strings.ContainsRune(ligand.MolBlock, 0) ||
				!strings.Contains(ligand.MolBlock, "M  END") {
				return request, errors.New("ligand batch contains an invalid mol block")
			}
			totalLigandBytes += len(ligand.Key) + len(ligand.MolBlock)
			if totalLigandBytes > maxStructureElectrostaticBatchBytes {
				return request, errors.New("ligand batch exceeds the bounded size limit")
			}
		}
		sort.Slice(request.LigandMolBlocks, func(left, right int) bool {
			return strings.ToUpper(request.LigandMolBlocks[left].Key) < strings.ToUpper(request.LigandMolBlocks[right].Key)
		})
	}
	extension := strings.ToLower(filepath.Ext(request.Filename))
	switch extension {
	case ".pdb", ".ent", ".cif", ".mmcif", ".pdbqt", ".pqr", ".sdf", ".mol", ".mol2", ".xyz", ".gro":
	default:
		return request, errors.New("structure filename is unsupported")
	}
	if request.PH < 0 || request.PH > 14 {
		return request, errors.New("ph must be between 0 and 14")
	}
	if request.IonicStrengthMolar < 0 || request.IonicStrengthMolar > 1 {
		return request, errors.New("ionic_strength_molar must be between 0 and 1")
	}
	request.PH = math.Round(request.PH*100) / 100
	request.IonicStrengthMolar = math.Round(request.IonicStrengthMolar*1000) / 1000
	if len(structureElectrostaticMapComponents(request)) == 0 {
		return request, errors.New("structure contains no supported protein or ligand atoms")
	}
	return request, nil
}

func structureElectrostaticRequestWeight(request structureElectrostaticMapRequest) int {
	weight := len(request.Content) + len(request.LigandMolBlock)
	for _, ligand := range request.LigandMolBlocks {
		weight += len(ligand.Key) + len(ligand.MolBlock)
	}
	return weight
}

type structureElectrostaticMapComponent struct {
	Role          string
	Key           string
	InputFilename string
	DXFilename    string
}

func structureElectrostaticIsBatchRequest(request structureElectrostaticMapRequest) bool {
	return request.ligandMolBlocksPresent || request.LigandMolBlocks != nil
}

func structureElectrostaticMapComponents(request structureElectrostaticMapRequest) []structureElectrostaticMapComponent {
	components := make([]structureElectrostaticMapComponent, 0, 1+len(request.LigandMolBlocks))
	if strings.HasPrefix(request.Content, "ATOM  ") || strings.Contains(request.Content, "\nATOM  ") {
		components = append(components, structureElectrostaticMapComponent{
			Role: "protein", DXFilename: structureElectrostaticDXFilename("protein"),
		})
	}
	if strings.TrimSpace(request.LigandMolBlock) != "" {
		components = append(components, structureElectrostaticMapComponent{
			Role: "ligand", InputFilename: "electrostatic-ligand.mol", DXFilename: structureElectrostaticDXFilename("ligand"),
		})
	}
	for index, ligand := range request.LigandMolBlocks {
		stem := "electrostatic-ligand-" + strconv.Itoa(index+1)
		components = append(components, structureElectrostaticMapComponent{
			Role: "ligand", Key: ligand.Key, InputFilename: stem + ".mol", DXFilename: stem + "-potential.dx",
		})
	}
	return components
}

func (s *Server) executeStructureElectrostaticMap(
	w http.ResponseWriter,
	parentContext context.Context,
	access workspace.KernelFrameAccess,
	request structureElectrostaticMapRequest,
) {
	runtimeRequest := newStructureElectrostaticRuntimeRequest(structureElectrostaticInputSHA256(request))
	runtimeRequest.Arguments = []string{
		"structure_electrostatic_map.py",
		"--input", "electrostatic-complex.pdb",
		"--protein-dx", structureElectrostaticDXFilename("protein"),
		"--ligand-dx", structureElectrostaticDXFilename("ligand"),
		"--report", "electrostatic-report.json",
		"--ph", strconv.FormatFloat(request.PH, 'f', 2, 64),
		"--ionic-strength", strconv.FormatFloat(request.IonicStrengthMolar, 'f', 3, 64),
	}
	if request.LigandMolBlock != "" {
		runtimeRequest.Arguments = append(runtimeRequest.Arguments, "--ligand", "electrostatic-ligand.mol")
	}
	for _, component := range structureElectrostaticMapComponents(request) {
		if component.Key != "" {
			runtimeRequest.Arguments = append(
				runtimeRequest.Arguments,
				"--ligand-component", component.Key, component.InputFilename, component.DXFilename,
			)
		}
	}
	components := structureElectrostaticMapComponents(request)
	runtimeRequest.ExpectedOutputs = []software.OutputWitness{
		{Path: "electrostatic-report.json", MinBytes: 2, Format: "json", RequiredJSONTrue: []string{"/contract", "/ok"}},
	}
	for _, component := range components {
		runtimeRequest.ExpectedOutputs = append(runtimeRequest.ExpectedOutputs, software.OutputWitness{
			Path: component.DXFilename, MinBytes: 100, Format: "text",
		})
	}
	rawCanonicalRequest, err := software.CanonicalRequestJSON(runtimeRequest)
	if err != nil {
		writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "runtime_request_invalid", "the electrostatic runtime request could not be admitted", false)
		return
	}
	var runtimeInput map[string]any
	if err := json.Unmarshal(rawCanonicalRequest, &runtimeInput); err != nil {
		writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "runtime_request_invalid", "the electrostatic runtime request could not be encoded", false)
		return
	}
	ctx, cancel := context.WithTimeout(parentContext, maxStructureElectrostaticTimeout)
	defer cancel()
	controller, plan, err := s.resolveSoftwareRuntime(ctx, runtimeInput)
	if err != nil {
		writeStructureElectrostaticMapError(w, http.StatusServiceUnavailable, "software_runtime_unavailable", "the APBS electrostatic runtime is not ready", true)
		return
	}
	operationID := uuid.NewString()
	provision, harness, err := s.provisionSoftwareRuntime(ctx, controller, plan, operationID)
	if err != nil {
		writeStructureElectrostaticSoftwareError(w, err)
		return
	}
	operationDir, mounts, protectedPaths, err := s.prepareStructureMinimizationWorkspace(access)
	if err != nil {
		writeStructureElectrostaticMapError(w, http.StatusServiceUnavailable, "workspace_unavailable", "the task-scoped electrostatic workspace could not be prepared", true)
		return
	}
	defer os.RemoveAll(operationDir)
	for path, data := range map[string][]byte{
		filepath.Join(operationDir, "electrostatic-complex.pdb"):      []byte(request.Content),
		filepath.Join(operationDir, "structure_electrostatic_map.py"): structureElectrostaticMapProgram,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "runtime_stage_failed", "the electrostatic runtime inputs could not be staged", false)
			return
		}
	}
	if request.LigandMolBlock != "" {
		if err := os.WriteFile(filepath.Join(operationDir, "electrostatic-ligand.mol"), []byte(request.LigandMolBlock), 0o600); err != nil {
			writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "runtime_stage_failed", "the ligand electrostatic input could not be staged", false)
			return
		}
	}
	for index, ligand := range request.LigandMolBlocks {
		component := components[len(components)-len(request.LigandMolBlocks)+index]
		if err := os.WriteFile(filepath.Join(operationDir, component.InputFilename), []byte(ligand.MolBlock), 0o600); err != nil {
			writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "runtime_stage_failed", "a ligand electrostatic input could not be staged", false)
			return
		}
	}
	kernelID := softwareRuntimeKernelID(operationID)
	worker, err := s.kernelManager.StartSession(kernelruntime.SessionSpec{
		KernelID: kernelID, OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, DelegateName: access.DelegateName,
		KernelKind: "analysis", Language: "python", Environment: plan.Environment,
		RuntimeGeneration: provision.Generation, WorkspaceDir: operationDir,
		Mounts: mounts, ProtectedPaths: protectedPaths,
	})
	if err != nil {
		writeStructureElectrostaticMapError(w, http.StatusServiceUnavailable, "kernel_start_failed", "the task-owned electrostatic kernel could not start", true)
		return
	}
	executionContext, cancelExecution := context.WithTimeout(ctx, time.Duration(plan.Request.TimeoutSeconds)*time.Second+softwareRuntimeExecutionTimeoutGrace)
	response, executeErr := worker.Execute(executionContext, harness, "agent")
	cancelExecution()
	closeErr := s.closeAgentKernel(kernelID)
	if closeErr != nil && executeErr == nil && response.Error == "" {
		writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "kernel_close_failed", "the task-owned electrostatic kernel could not be closed safely", true)
		return
	}
	if executeErr != nil && ctx.Err() != nil {
		writeStructureElectrostaticMapError(w, http.StatusGatewayTimeout, "electrostatic_map_timeout", "electrostatic potential calculation exceeded the bounded execution time", true)
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
		writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "runtime_receipt_invalid", "the electrostatic runtime returned an invalid execution receipt", false)
		return
	}
	report, reportErr := readStructureElectrostaticMapReport(operationDir)
	if !boolValue(visible["ok"], false) {
		message := "APBS could not calculate an electrostatic potential for this structure"
		if reportErr == nil && safeStructureElectrostaticMessage(report.Message) != "" {
			message = safeStructureElectrostaticMessage(report.Message)
		}
		writeStructureElectrostaticMapError(w, http.StatusUnprocessableEntity, "electrostatic_map_failed", message, false)
		return
	}
	if reportErr != nil || !report.Contract || report.ContractVersion != "2.0" || !report.OK || report.Engine != "APBS" || report.PotentialUnit != "kT/e" ||
		report.CalculationMode != "separate-components" || report.GridAlignment != "shared-frame" ||
		!structureElectrostaticReportMatchesRequest(report, request) {
		writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "electrostatic_report_invalid", "the APBS electrostatic report failed validation", false)
		return
	}
	potentialMaps := make(map[string]any, 2)
	ligandPotentialMaps := make(map[string]any, len(request.LigandMolBlocks))
	totalEncodedBytes := 0
	totalDXBytes := 0
	for _, component := range components {
		dx, err := readBoundedStructureFile(
			filepath.Join(operationDir, component.DXFilename),
			maxStructureElectrostaticDXBytes,
		)
		if err != nil || !utf8.Valid(dx) || !bytes.Contains(dx, []byte("class gridpositions counts")) || !bytes.Contains(dx, []byte("data follows")) {
			writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "electrostatic_dx_invalid", "the APBS OpenDX potential map failed validation", false)
			return
		}
		totalDXBytes += len(dx)
		if totalDXBytes > maxStructureElectrostaticTotalDXBytes {
			writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "electrostatic_dx_too_large", "the APBS potential maps exceed the interactive preview limit", false)
			return
		}
		encoded, err := encodeStructureElectrostaticDX(dx)
		totalEncodedBytes += len(encoded)
		if err != nil || len(encoded) > maxStructureElectrostaticEncodedBytes || totalEncodedBytes > maxStructureElectrostaticTotalEncoded {
			writeStructureElectrostaticMapError(w, http.StatusInternalServerError, "electrostatic_dx_too_large", "the compressed APBS potential map exceeds the interactive preview limit", false)
			return
		}
		if component.Key == "" {
			potentialMaps[component.Role] = map[string]any{"dx_gzip_base64": encoded}
		} else {
			ligandPotentialMaps[component.Key] = map[string]any{"dx_gzip_base64": encoded}
		}
	}
	writeJSON(
		w,
		http.StatusOK,
		structureElectrostaticMapResponsePayload(
			request,
			potentialMaps,
			ligandPotentialMaps,
			report,
			structureMinimizationRuntimeSummary(visible),
		),
	)
}

func structureElectrostaticMapResponsePayload(
	request structureElectrostaticMapRequest,
	potentialMaps map[string]any,
	ligandPotentialMaps map[string]any,
	report structureElectrostaticMapReport,
	runtime any,
) map[string]any {
	payload := map[string]any{
		"ok": true, "status": "completed", "encoding": "gzip+base64", "potential_maps": potentialMaps,
		"report": report, "runtime": runtime,
	}
	if structureElectrostaticIsBatchRequest(request) {
		potentialMaps["ligands"] = ligandPotentialMaps
	}
	return payload
}

func readStructureElectrostaticMapReport(operationDir string) (structureElectrostaticMapReport, error) {
	var report structureElectrostaticMapReport
	content, err := readBoundedStructureFile(filepath.Join(operationDir, "electrostatic-report.json"), maxStructureElectrostaticReportBytes)
	if err != nil || !utf8.Valid(content) {
		return report, errors.New("electrostatic report is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, errors.New("electrostatic report is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return report, errors.New("electrostatic report contains trailing data")
	}
	if len(report.ColorRange) != 2 {
		return report, errors.New("electrostatic report contract is incomplete")
	}
	return report, nil
}

func structureElectrostaticDXFilename(role string) string {
	return "electrostatic-" + role + "-potential.dx"
}

func structureElectrostaticReportMatchesRequest(report structureElectrostaticMapReport, request structureElectrostaticMapRequest) bool {
	components := structureElectrostaticMapComponents(request)
	_, digestErr := hex.DecodeString(report.InputSHA256)
	expectedInputSHA256 := structureElectrostaticInputSHA256(request)
	expectedProteinChargeMethod := "not applicable"
	if report.ProteinAtomCount > 0 {
		expectedProteinChargeMethod = "PDB2PQR AMBER with PROPKA protonation"
	}
	expectedLigandChargeMethod := "not applicable"
	if report.LigandAtomCount > 0 {
		expectedLigandChargeMethod = "RDKit Gasteiger with explicit hydrogens"
	}
	if len(components) == 0 || report.TotalAtomCount != report.ProteinAtomCount+report.LigandAtomCount ||
		report.ProteinAtomCount < 0 || report.LigandAtomCount < 0 || report.TotalAtomCount <= 0 ||
		report.ProteinAtomCount > maxStructureElectrostaticProteinAtoms ||
		report.LigandAtomCount > maxStructureElectrostaticLigandAtoms ||
		!boundedStructureElectrostaticText(report.EngineVersion, 64) ||
		report.PreparationEngine != "PDB2PQR + RDKit" ||
		!boundedStructureElectrostaticText(report.PreparationEngineVersion, 256) ||
		report.ProteinChargeMethod != expectedProteinChargeMethod ||
		report.LigandChargeMethod != expectedLigandChargeMethod ||
		len(report.InputSHA256) != sha256.Size*2 || digestErr != nil || report.InputSHA256 != expectedInputSHA256 ||
		len(report.ColorRange) != 2 || !finiteStructureElectrostaticValues(report.ColorRange) ||
		math.Abs(report.ColorRange[0]+5) > 1e-9 || math.Abs(report.ColorRange[1]-5) > 1e-9 ||
		report.ForceField != "AMBER" ||
		math.Abs(report.MeshSpacingAngstrom-0.65) > 1e-9 ||
		math.Abs(report.PH-request.PH) > 1e-9 || math.Abs(report.IonicStrengthMolar-request.IonicStrengthMolar) > 1e-9 {
		return false
	}
	expectedBaseRoles := make(map[string]bool, 2)
	expectedLigandKeys := make(map[string]bool, len(request.LigandMolBlocks))
	for _, component := range components {
		if component.Key == "" {
			expectedBaseRoles[component.Role] = true
		} else {
			expectedLigandKeys[component.Key] = true
		}
	}
	if (report.Grids.Protein != nil) != expectedBaseRoles["protein"] ||
		(report.Grids.Ligand != nil) != expectedBaseRoles["ligand"] ||
		len(report.Grids.Ligands) != len(expectedLigandKeys) || len(report.LigandAtomCounts) != len(expectedLigandKeys) {
		return false
	}
	if expectedBaseRoles["protein"] != (report.ProteinAtomCount > 0) {
		return false
	}
	if !structureElectrostaticIsBatchRequest(request) {
		if report.LigandAtomCount > maxStructureElectrostaticPerLigand {
			return false
		}
		if expectedBaseRoles["ligand"] != (report.LigandAtomCount > 0) {
			return false
		}
	}

	allGrids := make([]structureElectrostaticGridReport, 0, len(expectedBaseRoles)+len(report.Grids.Ligands))
	for role := range expectedBaseRoles {
		var grid *structureElectrostaticGridReport
		switch role {
		case "protein":
			grid = report.Grids.Protein
		case "ligand":
			grid = report.Grids.Ligand
		}
		if grid == nil || !validStructureElectrostaticGrid(*grid) {
			return false
		}
		allGrids = append(allGrids, *grid)
	}
	ligandAtomTotal := 0
	for key := range expectedLigandKeys {
		atomCount, countOK := report.LigandAtomCounts[key]
		grid, gridOK := report.Grids.Ligands[key]
		if !countOK || atomCount <= 0 || atomCount > maxStructureElectrostaticPerLigand || !gridOK || !validStructureElectrostaticGrid(grid) {
			return false
		}
		ligandAtomTotal += atomCount
		if ligandAtomTotal > maxStructureElectrostaticLigandAtoms {
			return false
		}
		allGrids = append(allGrids, grid)
	}
	if structureElectrostaticIsBatchRequest(request) && ligandAtomTotal != report.LigandAtomCount {
		return false
	}
	gridValueTotal := 0
	for index, grid := range allGrids {
		if gridValueTotal > maxStructureElectrostaticTotalGrids-grid.ValueCount {
			return false
		}
		gridValueTotal += grid.ValueCount
		if index > 0 && !sameStructureElectrostaticGridFrame(allGrids[0], grid) {
			return false
		}
	}
	if len(report.Warnings) > 32 {
		return false
	}
	for _, warning := range report.Warnings {
		if len(warning) == 0 || len(warning) > 256 || !utf8.ValidString(warning) || strings.ContainsRune(warning, 0) {
			return false
		}
	}
	return true
}

func boundedStructureElectrostaticText(value string, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed == value && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func finiteStructureElectrostaticValues(values []float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func validStructureElectrostaticGrid(grid structureElectrostaticGridReport) bool {
	if len(grid.Counts) != 3 || len(grid.Origin) != 3 || len(grid.Delta) != 3 || grid.ValueCount <= 0 {
		return false
	}
	valueCount := 1
	for _, count := range grid.Counts {
		if count <= 0 || valueCount > maxStructureElectrostaticGridValues/count {
			return false
		}
		valueCount *= count
	}
	if valueCount != grid.ValueCount {
		return false
	}
	for _, values := range [][]float64{grid.Origin, grid.Delta, []float64{grid.Minimum, grid.Maximum}} {
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return false
			}
		}
	}
	if grid.Minimum > grid.Maximum {
		return false
	}
	for _, step := range grid.Delta {
		if step <= 0 {
			return false
		}
	}
	return true
}

func sameStructureElectrostaticGridFrame(left, right structureElectrostaticGridReport) bool {
	for index := 0; index < 3; index++ {
		if left.Counts[index] != right.Counts[index] || math.Abs(left.Origin[index]-right.Origin[index]) > 1e-6 ||
			math.Abs(left.Delta[index]-right.Delta[index]) > 1e-6 {
			return false
		}
	}
	return true
}

func encodeStructureElectrostaticDX(dx []byte) (string, error) {
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return "", err
	}
	if _, err := writer.Write(dx); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(compressed.Bytes()), nil
}

func structureElectrostaticInputSHA256(request structureElectrostaticMapRequest) string {
	digest := sha256.New()
	writeStructureElectrostaticInputDigestPart(digest, structureElectrostaticInputDigestDomain)
	if structureElectrostaticIsBatchRequest(request) {
		writeStructureElectrostaticInputDigestPart(digest, "batch")
	} else {
		writeStructureElectrostaticInputDigestPart(digest, "legacy")
	}
	writeStructureElectrostaticInputDigestPart(digest, request.Content)
	if structureElectrostaticIsBatchRequest(request) {
		ligands := append([]structureElectrostaticLigandInput(nil), request.LigandMolBlocks...)
		sort.Slice(ligands, func(left, right int) bool {
			leftKey := strings.ToUpper(ligands[left].Key)
			rightKey := strings.ToUpper(ligands[right].Key)
			if leftKey == rightKey {
				return ligands[left].Key < ligands[right].Key
			}
			return leftKey < rightKey
		})
		for _, ligand := range ligands {
			writeStructureElectrostaticInputDigestPart(digest, ligand.Key)
			writeStructureElectrostaticInputDigestPart(digest, ligand.MolBlock)
		}
	} else {
		writeStructureElectrostaticInputDigestPart(digest, request.LigandMolBlock)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// Keep this domain-separated field framing byte-for-byte aligned with
// electrostatic_input_sha256 in the embedded Python runtime.
func writeStructureElectrostaticInputDigestPart(destination io.Writer, value string) {
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = io.WriteString(destination, value)
}

func structureElectrostaticMapCacheKey(access workspace.KernelFrameAccess, request structureElectrostaticMapRequest) string {
	digest := sha256.New()
	mode := "apbs-pdb2pqr-v2-separate-components"
	if structureElectrostaticIsBatchRequest(request) {
		mode += "-batch-v1"
	}
	for _, part := range []string{
		mode, access.UserID, access.Frame.ProjectID, access.Frame.ID, access.Frame.IncarnationID,
		access.RootFrameIncarnationID, request.Content, request.LigandMolBlock,
		strconv.FormatFloat(request.PH, 'f', 2, 64), strconv.FormatFloat(request.IonicStrengthMolar, 'f', 3, 64),
	} {
		writeStructureInteractionCacheKeyPart(digest, part)
	}
	for _, ligand := range request.LigandMolBlocks {
		writeStructureInteractionCacheKeyPart(digest, ligand.Key)
		writeStructureInteractionCacheKeyPart(digest, ligand.MolBlock)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func safeStructureElectrostaticMessage(message string) string {
	switch strings.TrimSpace(message) {
	case "structure contains no supported protein or ligand atoms",
		"protein charge preparation failed",
		"ligand charge preparation failed",
		"electrostatic grid generation failed",
		"electrostatic component grids could not be aligned",
		"electrostatic potential calculation failed",
		"electrostatic potential output exceeded the interactive limit":
		return strings.TrimSpace(message)
	default:
		return ""
	}
}

func writeStructureElectrostaticMapError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	writeJSON(w, status, map[string]any{
		"ok": false, "status": "failed", "code": code, "message": message, "retryable": retryable,
	})
}

func writeStructureElectrostaticSoftwareError(w http.ResponseWriter, err error) {
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
		"software_install_timeout":            "electrostatic runtime preparation exceeded its bounded timeout",
		"software_install_failed":             "electrostatic runtime preparation failed",
		"software_repair_failed":              "electrostatic runtime repair failed",
		"software_dependency_unavailable":     "a declared APBS or PDB2PQR dependency is unavailable",
		"software_environment_witness_failed": "the electrostatic environment witness did not produce a valid result",
		"software_request_unsupported":        "no registered provider satisfies the electrostatic runtime request",
		"software_import_missing":             "a required electrostatic runtime import is unavailable",
	}[code]
	if message == "" {
		message = "the verified electrostatic runtime could not be provisioned"
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"ok": false, "status": "failed", "code": code, "message": message,
		"recovery": recovery, "repair_scope": repairScope, "retryable": true,
	})
}

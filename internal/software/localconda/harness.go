package localconda

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"synon-go/internal/software"
)

const ResultPrefix = "SYNON_SOFTWARE_RESULT="

const (
	pythonHarnessPayloadPrefix = `_request = json.loads(base64.b64decode("`
	pythonHarnessPayloadSuffix = `").decode("utf-8"))`
)

type executionPayload struct {
	ProviderID              string                               `json:"provider_id"`
	Language                string                               `json:"language"`
	CompatibleReuse         bool                                 `json:"compatible_reuse,omitempty"`
	Environment             string                               `json:"environment"`
	Generation              string                               `json:"generation"`
	RequestDigest           string                               `json:"request_digest"`
	Provisioning            string                               `json:"provisioning"`
	Executable              string                               `json:"executable"`
	Arguments               []string                             `json:"args"`
	Stdin                   string                               `json:"stdin"`
	TimeoutSeconds          int64                                `json:"timeout_seconds"`
	ExpectedOutputs         []software.OutputWitness             `json:"expected_outputs"`
	Comparisons             []software.TabularComparisonContract `json:"comparisons,omitempty"`
	ScientificEvidence      *software.ScientificEvidenceRequest  `json:"scientific_evidence,omitempty"`
	ScientificProfileSHA256 string                               `json:"scientific_profile_sha256,omitempty"`
}

type StreamReceipt struct {
	Text      string `json:"text"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Truncated bool   `json:"truncated"`
}

type OutputReceipt struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type ScientificFileReceipt struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ScientificEvidenceReceipt is emitted by the trusted command host from
// bytes observed at execution time. It is later converted to the catalog's
// provider-independent Witness contract by the server.
type ScientificEvidenceReceipt struct {
	Engine        string                  `json:"engine"`
	EnginePackage string                  `json:"engine_package"`
	EngineVersion string                  `json:"engine_version"`
	ProfileSHA256 string                  `json:"profile_sha256"`
	CodeSHA256    string                  `json:"code_sha256"`
	WeightsSHA256 string                  `json:"weights_sha256,omitempty"`
	ScoreKind     string                  `json:"score_kind"`
	Inputs        []ScientificFileReceipt `json:"inputs"`
	Artifacts     []ScientificFileReceipt `json:"artifacts"`
}

type CleanupReceipt struct {
	ProcessGroupTerminated bool `json:"process_group_terminated"`
	ProcessTreeTerminated  bool `json:"process_tree_terminated"`
	TemporaryStreamsClosed bool `json:"temporary_streams_closed"`
}

type ExecutionReceipt struct {
	OK                 bool                       `json:"ok"`
	Code               string                     `json:"code"`
	ProviderID         string                     `json:"provider_id"`
	Environment        string                     `json:"environment"`
	Generation         string                     `json:"generation"`
	RequestDigest      string                     `json:"request_digest"`
	Provisioning       string                     `json:"provisioning"`
	Executable         string                     `json:"executable"`
	ExitCode           int                        `json:"exit_code"`
	TimedOut           bool                       `json:"timed_out"`
	StartedAt          string                     `json:"started_at"`
	FinishedAt         string                     `json:"finished_at"`
	Stdout             StreamReceipt              `json:"stdout"`
	Stderr             StreamReceipt              `json:"stderr"`
	Outputs            []OutputReceipt            `json:"outputs"`
	ScientificEvidence *ScientificEvidenceReceipt `json:"scientific_evidence,omitempty"`
	Cleanup            CleanupReceipt             `json:"cleanup"`
	Recovery           string                     `json:"recovery,omitempty"`
}

func BuildPythonHarness(plan software.Plan, provision software.ProvisionReceipt) (string, error) {
	request, err := software.NormalizeRequest(plan.Request)
	if err != nil {
		return "", err
	}
	wantDigest, err := software.RequestDigest(request)
	if err != nil {
		return "", err
	}
	wantEnvironment, err := software.EnvironmentName(ProviderID, request)
	if err != nil {
		return "", err
	}
	if plan.ProviderID != ProviderID || provision.ProviderID != ProviderID || provision.Environment != plan.Environment ||
		plan.RequestDigest != wantDigest || (plan.Environment != wantEnvironment && !plan.CompatibleReuse) ||
		(plan.CompatibleReuse && !strings.HasPrefix(plan.Environment, "swr-")) ||
		provision.Generation == "" || provision.Executable != request.Executable || !provision.Verified ||
		!provision.Preflight || !validProvisioningDisposition(provision.Disposition) {
		return "", errors.New("local conda execution authority is invalid")
	}
	payload := executionPayload{
		ProviderID: plan.ProviderID, Language: request.Language, CompatibleReuse: plan.CompatibleReuse,
		Environment: provision.Environment, Generation: provision.Generation,
		RequestDigest: plan.RequestDigest, Provisioning: provision.Disposition, Executable: request.Executable,
		Arguments: append([]string(nil), request.Arguments...), Stdin: request.Stdin,
		TimeoutSeconds:  request.TimeoutSeconds,
		ExpectedOutputs: append([]software.OutputWitness{}, request.ExpectedOutputs...),
		Comparisons:     append([]software.TabularComparisonContract{}, request.Comparisons...),
	}
	if request.ScientificEvidence != nil {
		profileSHA256, err := software.ScientificEvidenceDigest(*request.ScientificEvidence)
		if err != nil {
			return "", err
		}
		evidence := *request.ScientificEvidence
		evidence.Inputs = append([]software.ScientificFileWitness(nil), request.ScientificEvidence.Inputs...)
		evidence.Artifacts = append([]software.ScientificFileWitness(nil), request.ScientificEvidence.Artifacts...)
		evidence.CodePaths = append([]string(nil), request.ScientificEvidence.CodePaths...)
		payload.ScientificEvidence = &evidence
		payload.ScientificProfileSHA256 = profileSHA256
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if len(encoded) > 2*1024*1024 {
		return "", errors.New("local conda execution payload exceeds the bounded limit")
	}
	harness := strings.Replace(pythonHarness, "__PAYLOAD_BASE64__", base64.StdEncoding.EncodeToString(encoded), 1)
	harness = strings.Replace(harness, "__PYTHON_SOURCE_PREFLIGHT_SENTINEL__", pythonSourcePreflightSentinel(request), 1)
	return harness, nil
}

// pythonSourcePreflightSentinel makes a directly executed Python script a
// literal source unit of the same managed-interpreter kernel preflight that
// checks interactive Python. The branch never executes; the kernel owns
// parsing, imported-API inspection, and undefined-name diagnostics before the
// trusted argv harness can launch the script.
func pythonSourcePreflightSentinel(request software.Request) string {
	if request.Language != "python" || len(request.Arguments) == 0 {
		return "pass"
	}
	raw := strings.TrimSpace(request.Arguments[0])
	clean := filepath.ToSlash(filepath.Clean(raw))
	if raw == "" || strings.HasPrefix(raw, "-") || !strings.HasSuffix(strings.ToLower(clean), ".py") ||
		filepath.IsAbs(raw) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "pass"
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return "pass"
	}
	return "if False:\n    open(" + string(encoded) + ", encoding=\"utf-8\")"
}

// ValidatePythonHarness proves that a launcher was generated from the exact
// admitted software request and one verified immutable environment
// generation. The payload is inspected only to recover the provider-owned
// generation/disposition authority; rebuilding and byte-comparing the fixed
// harness rejects any changed argv, code, output witness, or cleanup logic.
func ValidatePythonHarness(request software.Request, environment, source string) error {
	request, err := software.NormalizeRequest(request)
	if err != nil {
		return err
	}
	environment = strings.TrimSpace(environment)
	prefixIndex := strings.Index(source, pythonHarnessPayloadPrefix)
	if prefixIndex < 0 || strings.Index(source[prefixIndex+len(pythonHarnessPayloadPrefix):], pythonHarnessPayloadPrefix) >= 0 {
		return errors.New("local conda execution launcher payload is unavailable")
	}
	payloadStart := prefixIndex + len(pythonHarnessPayloadPrefix)
	payloadEndOffset := strings.Index(source[payloadStart:], pythonHarnessPayloadSuffix)
	if payloadEndOffset < 0 {
		return errors.New("local conda execution launcher payload is incomplete")
	}
	payloadEnd := payloadStart + payloadEndOffset
	decoded, err := base64.StdEncoding.DecodeString(source[payloadStart:payloadEnd])
	if err != nil || len(decoded) == 0 || len(decoded) > 2*1024*1024 {
		return errors.New("local conda execution launcher payload is invalid")
	}
	var payload executionPayload
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return errors.New("local conda execution launcher payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("local conda execution launcher payload is invalid")
	}
	requestDigest, err := software.RequestDigest(request)
	if err != nil {
		return err
	}
	wantEnvironment, err := software.EnvironmentName(ProviderID, request)
	if err != nil || payload.Environment != environment ||
		(environment != wantEnvironment && !payload.CompatibleReuse) ||
		(payload.CompatibleReuse && !strings.HasPrefix(environment, "swr-")) ||
		payload.ProviderID != ProviderID || payload.RequestDigest != requestDigest {
		return errors.New("local conda execution launcher conflicts with the admitted request")
	}
	plan := software.Plan{
		ProviderID: ProviderID, Request: request, RequestDigest: requestDigest, Environment: environment,
		CompatibleReuse: payload.CompatibleReuse,
	}
	provision := software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: environment, Generation: payload.Generation,
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: payload.Provisioning,
	}
	expected, err := BuildPythonHarness(plan, provision)
	if err != nil || expected != source {
		return errors.New("local conda execution launcher conflicts with its immutable authority")
	}
	return nil
}

func ParseExecutionReceipt(stdout string) (ExecutionReceipt, bool, error) {
	var encoded string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, ResultPrefix) {
			encoded = strings.TrimSpace(strings.TrimPrefix(line, ResultPrefix))
		}
	}
	if encoded == "" {
		return ExecutionReceipt{}, false, nil
	}
	if len(encoded) > 3*1024*1024 {
		return ExecutionReceipt{}, true, errors.New("software execution receipt exceeds the bounded limit")
	}
	var receipt ExecutionReceipt
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return ExecutionReceipt{}, true, errors.New("software execution receipt is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ExecutionReceipt{}, true, errors.New("software execution receipt is invalid")
	}
	if receipt.ProviderID != ProviderID || receipt.Environment == "" || receipt.Generation == "" ||
		receipt.RequestDigest == "" || receipt.Executable == "" || receipt.Code == "" ||
		receipt.StartedAt == "" || receipt.FinishedAt == "" || !receipt.Cleanup.TemporaryStreamsClosed ||
		!validProvisioningDisposition(receipt.Provisioning) {
		return ExecutionReceipt{}, true, errors.New("software execution receipt is incomplete")
	}
	if receipt.OK && (!receipt.Cleanup.ProcessGroupTerminated || !receipt.Cleanup.ProcessTreeTerminated) {
		return ExecutionReceipt{}, true, errors.New("successful software execution receipt has incomplete process cleanup")
	}
	if receipt.OK && receipt.ScientificEvidence != nil &&
		(receipt.ScientificEvidence.Engine == "" || receipt.ScientificEvidence.EnginePackage == "" ||
			receipt.ScientificEvidence.EngineVersion == "" || receipt.ScientificEvidence.ProfileSHA256 == "" ||
			receipt.ScientificEvidence.CodeSHA256 == "" || receipt.ScientificEvidence.ScoreKind == "" ||
			len(receipt.ScientificEvidence.Inputs) == 0 || len(receipt.ScientificEvidence.Artifacts) == 0) {
		return ExecutionReceipt{}, true, errors.New("successful software execution receipt has incomplete scientific evidence")
	}
	return receipt, true, nil
}

func validProvisioningDisposition(value string) bool {
	return value == software.ProvisionDispositionReused || value == software.ProvisionDispositionInstalled ||
		value == software.ProvisionDispositionRepaired
}

const pythonHarness = `import base64
import csv
import datetime
import glob
import hashlib
import html.parser
import importlib.metadata
import json
import math
import os
import re
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import zlib

__PYTHON_SOURCE_PREFLIGHT_SENTINEL__

_request = json.loads(base64.b64decode("__PAYLOAD_BASE64__").decode("utf-8"))
_started = datetime.datetime.now(datetime.timezone.utc).isoformat()
_stdout_file = tempfile.TemporaryFile()
_stderr_file = tempfile.TemporaryFile()
_process = None
_timed_out = False
_cleanup_group = True
_cleanup_tree = True
_temporary_resources_closed = True
_matplotlib_config_dir = None
_code = "launch_failed"
_exit_code = -1
_recovery = "inspect_the_bounded_diagnostic_and_repair_this_same_provider_plan"
_scientific_receipt = None

_is_linux = sys.platform.startswith("linux")
_subreaper_enabled = False
if _is_linux:
    try:
        import ctypes
        _subreaper_enabled = ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) == 0
    except Exception:
        _subreaper_enabled = False

def _stream_receipt(stream, limit=65536):
    stream.flush()
    stream.seek(0, os.SEEK_END)
    size = stream.tell()
    digest = hashlib.sha256()
    stream.seek(0)
    while True:
        block = stream.read(1024 * 1024)
        if not block:
            break
        digest.update(block)
    stream.seek(max(0, size - limit))
    text = stream.read(limit).decode("utf-8", errors="replace")
    return {"text": text, "bytes": size, "sha256": digest.hexdigest(), "truncated": size > limit}

def _prepare_matplotlib_config():
    directory = tempfile.mkdtemp(prefix="synon-matplotlib-")
    path = os.path.join(directory, "matplotlibrc")
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(
            "font.family: sans-serif\n"
            "font.sans-serif: Noto Sans CJK SC, Noto Sans CJK JP, "
            "Droid Sans Fallback, DejaVu Sans\n"
            "axes.unicode_minus: False\n"
        )
    return directory

def _linux_descendants(root):
    if not _is_linux:
        return []
    descendants = []
    queue = [int(root)]
    seen = {int(root)}
    while queue:
        parent = queue.pop(0)
        try:
            with open("/proc/%d/task/%d/children" % (parent, parent), "r", encoding="ascii") as handle:
                children = [int(value) for value in handle.read().split()]
        except Exception:
            children = []
        for child in children:
            if child <= 0 or child in seen:
                continue
            seen.add(child)
            descendants.append(child)
            queue.append(child)
    return descendants

def _signal_pids(pids, value):
    ok = True
    for pid in reversed(pids):
        try:
            os.kill(pid, value)
        except ProcessLookupError:
            pass
        except Exception:
            ok = False
    return ok

def _reap_children(deadline):
    if not _is_linux or not _subreaper_enabled:
        return
    while time.monotonic() < deadline:
        reaped = False
        while True:
            try:
                pid, _status = os.waitpid(-1, os.WNOHANG)
            except ChildProcessError:
                return
            except Exception:
                return
            if pid == 0:
                break
            reaped = True
        if not _linux_descendants(os.getpid()):
            return
        if not reaped:
            time.sleep(0.02)

def _terminate_group(process):
    if process is None:
        return True, True
    if os.name == "posix":
        group_ok = True
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        except Exception:
            group_ok = False
        tree_ok = _signal_pids(_linux_descendants(os.getpid()), signal.SIGTERM)
        time.sleep(0.15)
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        except Exception:
            group_ok = False
        tree_ok = _signal_pids(_linux_descendants(os.getpid()), signal.SIGKILL) and tree_ok
        try:
            process.wait(timeout=5)
        except Exception:
            group_ok = False
        _reap_children(time.monotonic() + 2.0)
        survivors = _linux_descendants(os.getpid())
        if survivors:
            tree_ok = _signal_pids(survivors, signal.SIGKILL) and tree_ok
            _reap_children(time.monotonic() + 2.0)
            survivors = _linux_descendants(os.getpid())
        tree_ok = tree_ok and not survivors
        try:
            os.killpg(process.pid, 0)
            group_ok = False
        except ProcessLookupError:
            pass
        except PermissionError:
            group_ok = False
        return group_ok, tree_ok
    try:
        subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5, check=False)
        process.wait(timeout=5)
        return True, True
    except Exception:
        try:
            process.kill()
            process.wait(timeout=5)
            return True, True
        except Exception:
            return False, False

def _json_pointer(document, pointer):
    current = document
    for raw in pointer.split("/")[1:]:
        token = raw.replace("~1", "/").replace("~0", "~")
        if isinstance(current, dict) and token in current:
            current = current[token]
        elif isinstance(current, list) and token.isdigit() and int(token) < len(current):
            current = current[int(token)]
        else:
            raise ValueError("json_assertion_missing:" + pointer)
    return current

def _read_table(path, delimiter):
    with open(path, "r", encoding="utf-8-sig", newline="") as handle:
        rows = list(csv.reader(handle, delimiter=delimiter))
    if not rows or not rows[0] or any(not str(value).strip() for value in rows[0]):
        raise ValueError("tabular_header_missing")
    header = [str(value).strip() for value in rows[0]]
    lowered = [value.lower() for value in header]
    if len(set(lowered)) != len(lowered):
        raise ValueError("tabular_header_duplicate")
    for row in rows[1:]:
        if len(row) != len(header):
            raise ValueError("tabular_row_width_mismatch")
        if any("\x00" in value for value in row):
            raise ValueError("tabular_nul_byte")
    return header, rows[1:]

def _validate_xyz(path, minimum_records):
    with open(path, "r", encoding="utf-8-sig") as handle:
        lines = handle.read().splitlines()
    index = 0
    frames = 0
    while index < len(lines):
        while index < len(lines) and not lines[index].strip():
            index += 1
        if index >= len(lines):
            break
        try:
            atoms = int(lines[index].strip())
        except Exception as error:
            raise ValueError("xyz_atom_count_invalid") from error
        if atoms <= 0 or atoms > 1000000 or index + atoms + 1 >= len(lines):
            raise ValueError("xyz_frame_length_invalid")
        index += 2
        for _ in range(atoms):
            fields = lines[index].split()
            if len(fields) < 4 or not re.fullmatch(r"[A-Za-z][A-Za-z]?", fields[0]):
                raise ValueError("xyz_atom_record_invalid")
            try:
                coordinates = [float(value) for value in fields[1:4]]
            except Exception as error:
                raise ValueError("xyz_coordinate_invalid") from error
            if not all(math.isfinite(value) for value in coordinates):
                raise ValueError("xyz_coordinate_nonfinite")
            index += 1
        frames += 1
    if frames < max(1, int(minimum_records or 0)):
        raise ValueError("xyz_record_count_below_minimum")
    return frames

def _validate_png(path):
    with open(path, "rb") as handle:
        data = handle.read()
    if len(data) < 45 or data[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError("png_signature_invalid")
    offset = 8
    chunks = []
    width = height = 0
    while offset + 12 <= len(data):
        length = struct.unpack(">I", data[offset:offset + 4])[0]
        kind = data[offset + 4:offset + 8]
        end = offset + 12 + length
        if length > 512 * 1024 * 1024 or end > len(data):
            raise ValueError("png_chunk_invalid")
        payload = data[offset + 8:offset + 8 + length]
        checksum = struct.unpack(">I", data[offset + 8 + length:end])[0]
        if zlib.crc32(kind + payload) & 0xffffffff != checksum:
            raise ValueError("png_crc_invalid")
        chunks.append(kind)
        if kind == b"IHDR":
            if length != 13:
                raise ValueError("png_ihdr_invalid")
            width, height = struct.unpack(">II", payload[:8])
        offset = end
        if kind == b"IEND":
            break
    if not chunks or chunks[0] != b"IHDR" or chunks[-1] != b"IEND" or width <= 0 or height <= 0 or offset != len(data):
        raise ValueError("png_structure_invalid")

class _HTMLWitnessParser(html.parser.HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.tags = set()
        self.visible = []
    def handle_starttag(self, tag, attrs):
        self.tags.add(str(tag).lower())
    def handle_data(self, data):
        if str(data).strip():
            self.visible.append(str(data).strip())

def _detected_format(relative, explicit):
    if explicit:
        return explicit
    extension = os.path.splitext(relative)[1].lower().lstrip(".")
    return extension if extension in {"json", "csv", "tsv", "xyz", "png", "html"} else "binary"

def _validate_output_file(candidate, witness):
    relative = witness["path"]
    format_name = _detected_format(relative, str(witness.get("format") or ""))
    minimum_records = int(witness.get("min_records") or 0)
    if format_name == "json":
        with open(candidate, "r", encoding="utf-8-sig") as handle:
            document = json.load(handle, parse_constant=lambda value: (_ for _ in ()).throw(ValueError("json_nonfinite:" + value)))
        for pointer in witness.get("required_json_true") or []:
            if _json_pointer(document, pointer) is not True:
                raise ValueError("json_assertion_not_true:" + pointer)
    elif format_name in {"csv", "tsv"}:
        _header, rows = _read_table(candidate, "," if format_name == "csv" else "\t")
        if len(rows) < minimum_records:
            raise ValueError("tabular_record_count_below_minimum")
    elif format_name == "xyz":
        _validate_xyz(candidate, minimum_records)
    elif format_name == "png":
        _validate_png(candidate)
    elif format_name == "html":
        with open(candidate, "r", encoding="utf-8-sig") as handle:
            source = handle.read()
        parser = _HTMLWitnessParser()
        parser.feed(source)
        parser.close()
        if "html" not in parser.tags or "body" not in parser.tags or not parser.visible:
            raise ValueError("html_document_incomplete")
    elif format_name == "text":
        with open(candidate, "r", encoding="utf-8-sig") as handle:
            handle.read()
    return format_name

def _output_receipts(root, witnesses):
    receipts = []
    failures = []
    root_real = os.path.realpath(root)
    for witness in witnesses:
        relative = witness["path"]
        candidate = os.path.realpath(os.path.join(root_real, relative))
        try:
            inside = os.path.commonpath([root_real, candidate]) == root_real
        except ValueError:
            inside = False
        if not inside or not os.path.isfile(candidate):
            failures.append(relative)
            continue
        size = os.path.getsize(candidate)
        if size < int(witness.get("min_bytes", 0)):
            failures.append(relative)
            continue
        try:
            _validate_output_file(candidate, witness)
        except Exception as error:
            failures.append(relative + ":" + str(error))
            continue
        digest = hashlib.sha256()
        with open(candidate, "rb") as handle:
            while True:
                block = handle.read(1024 * 1024)
                if not block:
                    break
                digest.update(block)
        receipts.append({"path": relative, "bytes": size, "sha256": digest.hexdigest()})
    return receipts, failures

def _derived_comparison_columns(header, rows):
    markers = (
        "relative", "delta", "difference", "ratio", "fold_change", "foldchange",
        "rank", "normalized", "percent_change", "pct_change", "change_pct",
        "deviation_pct", "dev_pct",
    )
    result = []
    for index, column in enumerate(header):
        normalized = re.sub(r"[^a-z0-9]+", "_", column.lower()).strip("_")
        if not any(
            normalized == marker
            or normalized.startswith(marker + "_")
            or normalized.endswith("_" + marker)
            or ("_" + marker + "_") in normalized
            for marker in markers
        ):
            continue
        numeric = bool(rows)
        for row in rows:
            try:
                numeric = numeric and math.isfinite(float(row[index]))
            except Exception:
                numeric = False
        if numeric:
            result.append(column)
    return result

def _validate_comparisons(root, witnesses, comparisons):
    contracts = {entry["path"]: entry for entry in comparisons or []}
    witness_by_path = {entry["path"]: entry for entry in witnesses or []}
    tabular = {}
    missing_contracts = []
    for relative, witness in witness_by_path.items():
        format_name = _detected_format(relative, str(witness.get("format") or ""))
        if format_name not in {"csv", "tsv"}:
            continue
        path = _workspace_file(root, relative)
        tabular[relative] = _read_table(path, "," if format_name == "csv" else "\t")
        derived = _derived_comparison_columns(tabular[relative][0], tabular[relative][1])
        if derived and relative not in contracts:
            missing_contracts.append(relative + ":" + ",".join(derived))
    if missing_contracts:
        raise ValueError("comparison_contract_missing:" + ";".join(missing_contracts))
    for relative, contract in contracts.items():
        if relative not in tabular:
            raise ValueError("comparison_table_unavailable:" + relative)
        header, rows = tabular[relative]
        if not rows:
            raise ValueError("comparison_table_empty:" + relative)
        positions = {column.lower(): index for index, column in enumerate(header)}
        roles = {}
        for role in ("derived_columns", "basis_columns", "group_columns"):
            columns = contract.get(role) or []
            missing = [column for column in columns if column.lower() not in positions]
            if missing:
                raise ValueError("comparison_" + role + "_missing:" + relative + ":" + ",".join(missing))
            roles[role] = [positions[column.lower()] for column in columns]
        detected = {column.lower() for column in _derived_comparison_columns(header, rows)}
        declared = {column.lower() for column in contract.get("derived_columns") or []}
        if not detected.issubset(declared):
            raise ValueError("comparison_derived_columns_incomplete:" + relative)
        groups = {}
        for row in rows:
            group = tuple(row[index].strip() for index in roles["group_columns"])
            basis = tuple(row[index].strip() for index in roles["basis_columns"])
            if any(not value for value in basis):
                raise ValueError("comparison_basis_empty:" + relative)
            groups.setdefault(group, set()).add(basis)
            for index in roles["derived_columns"]:
                try:
                    value = float(row[index])
                except Exception as error:
                    raise ValueError("comparison_value_not_numeric:" + relative) from error
                if not math.isfinite(value):
                    raise ValueError("comparison_value_nonfinite:" + relative)
        incompatible = [len(bases) for bases in groups.values() if len(bases) != 1]
        if incompatible:
            group_columns = contract.get("group_columns") or []
            group_label = ",".join(group_columns) if group_columns else "<all_rows>"
            raise ValueError(
                "comparison_basis_not_identical:" + relative
                + ":group_columns=" + group_label
                + ":max_basis_variants=" + str(max(incompatible))
            )

def _workspace_file(root, relative):
    root_real = os.path.realpath(root)
    candidate = os.path.realpath(os.path.join(root_real, relative))
    try:
        inside = os.path.commonpath([root_real, candidate]) == root_real
    except ValueError:
        inside = False
    if not inside or not os.path.isfile(candidate):
        raise ValueError("missing_or_unsafe_scientific_file:" + str(relative))
    if os.path.getsize(candidate) > 512 * 1024 * 1024:
        raise ValueError("scientific_file_exceeds_bound:" + str(relative))
    return candidate

def _prepare_output_directories(root, witnesses):
    root_real = os.path.realpath(root)
    for witness in witnesses:
        relative = str(witness.get("path") or "")
        candidate = os.path.realpath(os.path.join(root_real, relative))
        try:
            inside = os.path.commonpath([root_real, candidate]) == root_real
        except ValueError:
            inside = False
        if not inside:
            raise ValueError("unsafe_output_directory:" + relative)
        parent = os.path.dirname(candidate)
        if parent and parent != root_real:
            os.makedirs(parent, mode=0o700, exist_ok=True)

def _file_sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            block = handle.read(1024 * 1024)
            if not block:
                break
            digest.update(block)
    return digest.hexdigest()

def _typed_file_receipts(root, witnesses):
    receipts = []
    for witness in witnesses:
        relative = witness["path"]
        receipts.append({
            "kind": witness["kind"],
            "path": relative,
            "sha256": _file_sha256(_workspace_file(root, relative)),
        })
    return receipts

def _engine_package_version(prefix, package_name):
    normalized = str(package_name).strip().lower().replace("_", "-")
    for metadata_path in sorted(glob.glob(os.path.join(prefix, "conda-meta", "*.json"))):
        try:
            with open(metadata_path, "r", encoding="utf-8") as handle:
                metadata = json.load(handle)
            current = str(metadata.get("name") or "").strip().lower().replace("_", "-")
            version = str(metadata.get("version") or "").strip()
            if current == normalized and version:
                return version
        except Exception:
            continue
    try:
        version = str(importlib.metadata.version(package_name)).strip()
        if version:
            return version
    except Exception:
        pass
    raise ValueError("engine_package_version_unavailable:" + str(package_name))

def _scientific_preflight(root, prefix, resolved):
    evidence = _request.get("scientific_evidence")
    if not evidence:
        return None
    profile_sha256 = str(_request.get("scientific_profile_sha256") or "")
    if len(profile_sha256) != 64:
        raise ValueError("scientific_profile_digest_invalid")
    inputs = _typed_file_receipts(root, evidence.get("inputs") or [])
    code_digest = hashlib.sha256()
    code_digest.update(str(_request["request_digest"]).encode("ascii"))
    code_digest.update(b"\x00executable\x00")
    code_digest.update(bytes.fromhex(_file_sha256(resolved)))
    for relative in evidence.get("code_paths") or []:
        code_digest.update(b"\x00code\x00" + str(relative).encode("utf-8") + b"\x00")
        code_digest.update(bytes.fromhex(_file_sha256(_workspace_file(root, relative))))
    weights_sha256 = ""
    if evidence.get("weights_path"):
        weights_sha256 = _file_sha256(_workspace_file(root, evidence["weights_path"]))
    return {
        "engine": evidence["engine"],
        "engine_package": evidence["engine_package"],
        "engine_version": _engine_package_version(prefix, evidence["engine_package"]),
        "profile_sha256": profile_sha256,
        "code_sha256": code_digest.hexdigest(),
        "weights_sha256": weights_sha256,
        "score_kind": evidence["score_kind"],
        "inputs": inputs,
        "artifacts": [],
    }

def _complete_scientific_receipt(root, preflight, outputs):
    evidence = _request.get("scientific_evidence")
    if not evidence:
        return None
    if not preflight:
        raise ValueError("scientific_preflight_missing")
    outputs_by_path = {output["path"]: output for output in outputs}
    artifacts = []
    for witness in evidence.get("artifacts") or []:
        output = outputs_by_path.get(witness["path"])
        if not output:
            raise ValueError("scientific_artifact_receipt_missing:" + str(witness["path"]))
        artifacts.append({
            "kind": witness["kind"], "path": witness["path"], "sha256": output["sha256"],
        })
    result = dict(preflight)
    result["artifacts"] = artifacts
    return result

try:
    _prefix = os.path.realpath(os.environ.get("CONDA_PREFIX", ""))
    _resolved = shutil.which(_request["executable"])
    if not _resolved:
        _code = "executable_missing"
        _recovery = "repair_the_declared_packages_in_this_same_local_conda_plan"
    else:
        _resolved = os.path.realpath(_resolved)
        try:
            _inside_prefix = bool(_prefix) and os.path.commonpath([_prefix, _resolved]) == _prefix
        except ValueError:
            _inside_prefix = False
        if not _inside_prefix:
            _code = "executable_outside_managed_environment"
            _recovery = "install_the_executable_into_this_same_local_conda_environment"
        else:
            _prepare_output_directories(os.getcwd(), _request.get("expected_outputs") or [])
            try:
                _scientific_receipt = _scientific_preflight(os.getcwd(), _prefix, _resolved)
            except Exception as _scientific_error:
                _stderr_file.write((type(_scientific_error).__name__ + ": " + str(_scientific_error)).encode("utf-8", errors="replace"))
                _code = "scientific_evidence_invalid"
                _recovery = "repair_the_declared_scientific_inputs_engine_package_code_or_weights_in_this_same_provider_plan"
            _creation_flags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
            # Bound nested native math runtimes to one thread per launched
            # process. The caller may already parallelize independent work;
            # allowing every child to expand to all host CPUs multiplies that
            # parallelism and can make the workstation unusable. These are
            # process-local limits and never mutate the service environment.
            _child_env = os.environ.copy()
            for _thread_variable in (
                "OMP_NUM_THREADS", "OMP_THREAD_LIMIT", "OPENBLAS_NUM_THREADS",
                "MKL_NUM_THREADS", "BLIS_NUM_THREADS", "VECLIB_MAXIMUM_THREADS",
                "NUMEXPR_NUM_THREADS", "RAYON_NUM_THREADS",
            ):
                _child_env[_thread_variable] = "1"
            _child_env["OMP_DYNAMIC"] = "FALSE"
            _child_env["OMP_MAX_ACTIVE_LEVELS"] = "1"
            if _request.get("language") == "python":
                _matplotlib_config_dir = _prepare_matplotlib_config()
                _child_env["MPLCONFIGDIR"] = _matplotlib_config_dir
            if _code != "scientific_evidence_invalid":
                _process = subprocess.Popen(
                    [_resolved] + list(_request.get("args") or []),
                    stdin=subprocess.PIPE if _request.get("stdin") else subprocess.DEVNULL,
                    stdout=_stdout_file,
                    stderr=_stderr_file,
                    cwd=os.getcwd(),
                    env=_child_env,
                    shell=False,
                    start_new_session=(os.name == "posix"),
                    creationflags=_creation_flags,
                )
            try:
                if _process is not None:
                    _communicate = {
                        "input": _request.get("stdin", "").encode("utf-8") if _request.get("stdin") else None,
                    }
                    if float(_request.get("timeout_seconds") or 0) > 0:
                        _communicate["timeout"] = float(_request["timeout_seconds"])
                    _process.communicate(**_communicate)
                    _exit_code = int(_process.returncode)
                    _code = "completed" if _exit_code == 0 else "nonzero_exit"
                    if _exit_code != 0:
                        _recovery = "inspect_stderr_then_repair_inputs_or_packages_without_switching_provider"
            except subprocess.TimeoutExpired:
                _timed_out = True
                _code = "timeout"
                _recovery = "inspect_partial_outputs_then_change_the_explicit_deadline_or_workload"
            finally:
                _cleanup_group, _cleanup_tree = _terminate_group(_process)
except Exception as _error:
    _stderr_file.write((type(_error).__name__ + ": " + str(_error)).encode("utf-8", errors="replace"))
    _code = "launch_failed"
finally:
    _outputs, _output_failures = _output_receipts(os.getcwd(), _request.get("expected_outputs") or [])
    if not _output_failures and _code == "completed":
        try:
            _validate_comparisons(os.getcwd(), _request.get("expected_outputs") or [], _request.get("comparisons") or [])
        except Exception as _quality_error:
            _stderr_file.write((type(_quality_error).__name__ + ": " + str(_quality_error)).encode("utf-8", errors="replace"))
            _code = "quality_contract_failed"
            _recovery = "repair_the_result_basis_or_executable_assertions_then_rerun_this_same_provider_plan"
    if _output_failures:
        _stderr_file.write(("output_validation_failures: " + ";".join(_output_failures)).encode("utf-8", errors="replace"))
    if not _output_failures and _code == "completed" and _request.get("scientific_evidence"):
        try:
            _scientific_receipt = _complete_scientific_receipt(os.getcwd(), _scientific_receipt, _outputs)
        except Exception as _scientific_error:
            _stderr_file.write((type(_scientific_error).__name__ + ": " + str(_scientific_error)).encode("utf-8", errors="replace"))
            _code = "scientific_evidence_invalid"
            _recovery = "repair_the_declared_scientific_output_witnesses_in_this_same_provider_plan"
    if _matplotlib_config_dir:
        try:
            shutil.rmtree(_matplotlib_config_dir)
        except Exception as _cleanup_error:
            _temporary_resources_closed = False
            _stderr_file.write((type(_cleanup_error).__name__ + ": " + str(_cleanup_error)).encode("utf-8", errors="replace"))
    if not _output_failures and _code == "completed" and _cleanup_group and _cleanup_tree and _temporary_resources_closed:
        _ok = True
        _recovery = ""
    else:
        _ok = False
        if _output_failures and _code == "completed":
            _code = "output_validation_failed"
            _recovery = "repair_the_computation_until_every_declared_output_witness_passes"
    _stdout_receipt = _stream_receipt(_stdout_file)
    _stderr_receipt = _stream_receipt(_stderr_file)
    _stdout_file.close()
    _stderr_file.close()
    _receipt = {
        "ok": _ok,
        "code": _code,
        "provider_id": _request["provider_id"],
        "environment": _request["environment"],
        "generation": _request["generation"],
        "request_digest": _request["request_digest"],
        "provisioning": _request["provisioning"],
        "executable": _request["executable"],
        "exit_code": _exit_code,
        "timed_out": _timed_out,
        "started_at": _started,
        "finished_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "stdout": _stdout_receipt,
        "stderr": _stderr_receipt,
        "outputs": _outputs,
        "scientific_evidence": _scientific_receipt,
        "cleanup": {"process_group_terminated": _cleanup_group, "process_tree_terminated": _cleanup_tree, "temporary_streams_closed": _temporary_resources_closed},
        "recovery": _recovery,
    }
    print("SYNON_SOFTWARE_RESULT=" + json.dumps(_receipt, ensure_ascii=False, separators=(",", ":"), sort_keys=True), flush=True)
    if not _ok:
        raise RuntimeError("software_runtime_failed:" + _code)
`

func (r ExecutionReceipt) ValidateAgainst(plan software.Plan, provision software.ProvisionReceipt) error {
	if r.ProviderID != plan.ProviderID || r.Environment != plan.Environment || r.Generation != provision.Generation ||
		r.RequestDigest != plan.RequestDigest || r.Provisioning != provision.Disposition ||
		r.Executable != plan.Request.Executable {
		return fmt.Errorf("software execution receipt conflicts with its admitted plan")
	}
	return nil
}

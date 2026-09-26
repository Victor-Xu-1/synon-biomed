package kernelcontract

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/executionprep"
)

const (
	BashSourcePrefix = "# synon-bash-command-v1:"
	BashExitPrefix   = "__SYNON_BASH_EXIT_V1__:"
	MaxBashBytes     = 256 * 1024
)

// BashPythonWrapper is the single canonical execution envelope for a Bash
// command owned by the confined Python kernel. The model-authored command is
// encoded as data, never interpolated into executable Python source.
func BashPythonWrapper(command string) (string, error) {
	return bashPythonWrapper(command, bashStreamingPump)
}

// BashObservationPythonWrapper is the same canonical envelope with an explicit
// native startup contract. Privileged mode disables imported functions,
// SHELLOPTS and BASH_ENV; no profile runs before the user source's bindings.
// It neither changes languages nor replaces the durable executor.
func BashObservationPythonWrapper(command string, plan *executionprep.Observation) (string, error) {
	if !plan.Matches("bash", command) {
		return "", executionprep.ErrObservationUnproved
	}
	result, err := executionprep.Analyze(context.Background(), executionprep.Request{Language: "bash", Source: command}, nil)
	if err != nil || !result.Observation.Matches("bash", command) {
		return "", executionprep.ErrObservationUnproved
	}
	return bashPythonWrapper(command, bashStreamingPump, true)
}

// The legacy pump is accepted only when decoding immutable historical source;
// new executions always use the streaming pump.
const bashLegacyPump = `def _synon_pump(_synon_source, _synon_target):
    while True:
        _synon_chunk = _synon_source.read(8192)
        if not _synon_chunk:
            break
        _synon_target.write(_synon_chunk)
        _synon_target.flush()
`

const bashStreamingPump = `def _synon_pump(_synon_source, _synon_target):
    import codecs
    _synon_decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
    try:
        while True:
            _synon_raw = _synon_source.buffer.read1(8192)
            if not _synon_raw:
                break
            _synon_chunk = _synon_decoder.decode(_synon_raw)
            if _synon_chunk:
                _synon_target.write(_synon_chunk)
                _synon_target.flush()
        _synon_tail = _synon_decoder.decode(b"", final=True)
        if _synon_tail:
            _synon_target.write(_synon_tail)
            _synon_target.flush()
    finally:
        _synon_source.close()
`

func bashPythonWrapper(command, pump string, observation ...bool) (string, error) {
	if strings.TrimSpace(command) == "" || len(command) > MaxBashBytes || strings.ContainsRune(command, '\x00') {
		return "", errors.New("bash command must be 1-262144 bytes without NUL characters")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	arguments := `["/bin/bash", "-lc", _synon_script]`
	if len(observation) > 0 && observation[0] {
		arguments = `["/bin/bash", "--noprofile", "--norc", "-p", "-c", _synon_script]`
	}
	return fmt.Sprintf(`%s%s
import base64 as _synon_b64
import subprocess as _synon_subprocess
import sys as _synon_sys
import threading as _synon_threading

_synon_command = _synon_b64.b64decode(%q, validate=True).decode("utf-8")
_synon_script = "set -e\nset -o pipefail\n" + _synon_command
_synon_process = _synon_subprocess.Popen(
    %s,
    stdout=_synon_subprocess.PIPE,
    stderr=_synon_subprocess.PIPE,
    text=True,
    bufsize=1,
)

%s
_synon_stdout_thread = _synon_threading.Thread(target=_synon_pump, args=(_synon_process.stdout, _synon_sys.stdout), daemon=True)
_synon_stderr_thread = _synon_threading.Thread(target=_synon_pump, args=(_synon_process.stderr, _synon_sys.stderr), daemon=True)
_synon_stdout_thread.start()
_synon_stderr_thread.start()
_synon_exit_code = _synon_process.wait()
_synon_stdout_thread.join()
_synon_stderr_thread.join()
_synon_sys.stderr.write("\n%s" + str(_synon_exit_code) + "\n")
_synon_sys.stderr.flush()
`, BashSourcePrefix, encoded, encoded, arguments, pump, BashExitPrefix), nil
}

// BashCommandFromWrapper verifies the complete canonical envelope before
// returning its immutable model-authored source identity.
func BashCommandFromWrapper(source string) (string, error) {
	firstLine, _, _ := strings.Cut(source, "\n")
	if !strings.HasPrefix(firstLine, BashSourcePrefix) {
		return "", errors.New("bash durable source envelope is invalid")
	}
	encoded := strings.TrimPrefix(firstLine, BashSourcePrefix)
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(MaxBashBytes) {
		return "", errors.New("bash durable source envelope is invalid")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > MaxBashBytes || strings.ContainsRune(string(raw), '\x00') {
		return "", errors.New("bash durable source envelope is invalid")
	}
	command := string(raw)
	canonical, err := BashPythonWrapper(command)
	if err != nil {
		return "", err
	}
	if canonical != source {
		observational, observationErr := bashPythonWrapper(command, bashStreamingPump, true)
		if observationErr == nil && observational == source {
			return command, nil
		}
		legacy, legacyErr := bashPythonWrapper(command, bashLegacyPump)
		if legacyErr == nil && legacy == source {
			return command, nil
		}
		return "", errors.New("bash durable source envelope is not canonical")
	}
	return command, nil
}

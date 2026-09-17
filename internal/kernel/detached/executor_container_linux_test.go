//go:build linux

package detached

import (
	"strings"
	"testing"

	"synon-go/internal/kernelcontract"
)

func TestContainerExecutionCarriesRealBashExitReceipt(t *testing.T) {
	stderr := containerExecutionProtocolStderr(kernelcontract.BashTool, "diagnostic", 17)
	if !strings.Contains(stderr, "diagnostic") || !strings.HasSuffix(stderr, "\n"+kernelcontract.BashExitPrefix+"17\n") {
		t.Fatalf("stderr=%q", stderr)
	}
	if python := containerExecutionProtocolStderr(kernelcontract.PythonTool, "python diagnostic", 17); python != "python diagnostic" {
		t.Fatalf("Python stderr changed: %q", python)
	}
}

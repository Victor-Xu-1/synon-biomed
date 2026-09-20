package executionprep

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"os/exec"
	"runtime"
)

//go:embed powershell.ps1
var powerShellObservationFunctions string

// NativePowerShell never treats an interop executable on another platform or a
// shell-language compatibility implementation as a native parser/runtime.
func NativePowerShell() (string, error) {
	names := []string{"pwsh", "powershell"}
	if runtime.GOOS == "windows" {
		names = []string{"pwsh.exe", "powershell.exe"}
	}
	for _, name := range names {
		if executable, err := exec.LookPath(name); err == nil {
			return executable, nil
		}
	}
	return "", errors.New("native PowerShell runtime unavailable")
}

// PowerShellParser reads base64 source as data from stdin; parsing never invokes
// the source, even when the AST contains calls, redirections or malformed code.
func PowerShellParser() string {
	return powerShellObservationFunctions + `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$source = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$operations = Get-SynonObservation $source
$facts = @()
if ($null -ne $operations) {
    $facts = @(@{kind='observation'; name='synon.execution-observation.v1'; args=@($operations)})
}
[Console]::Out.Write((Microsoft.PowerShell.Utility\ConvertTo-Json -InputObject $facts -Compress -Depth 5))
`
}

// GuardedPowerShell keeps the command in its existing native execution process.
// It validates the exact source and effective cmdlet identities before invoking
// it. It does not grant filesystem, host, network or scientific permissions.
func GuardedPowerShell(source string, plan *Observation) (string, error) {
	if !plan.Matches("powershell", source) {
		return "", ErrObservationUnproved
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(source))
	return powerShellObservationFunctions + `
$ErrorActionPreference = 'Stop'
$source = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + encoded + `'))
$operations = Get-SynonObservation $source
if ($null -eq $operations) { throw 'diagnostic_source_unproved' }
Assert-SynonObservationBindings $operations
& ([ScriptBlock]::Create($source))
`, nil
}

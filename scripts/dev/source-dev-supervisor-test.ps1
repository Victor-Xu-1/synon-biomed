$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'source-dev-supervisor.ps1') -TestMode `
  -FailureThreshold 3 -StartupGraceSeconds 30 -RestartCooldownSeconds 90

function Assert-Equal {
  param($Expected, $Actual, [string]$Message)
  if ($Expected -ne $Actual) {
    throw "$Message (expected=$Expected actual=$Actual)"
  }
}

$start = [datetime]'2026-08-03T00:00:00Z'
$state = New-SupervisorState -NowUtc $start

Assert-Equal $false (Update-SupervisorState $state $false $false $start.AddSeconds(10)) 'startup grace must suppress restart'
Assert-Equal 0 $state.FailureCount 'startup grace must not consume failure budget'

Assert-Equal $false (Update-SupervisorState $state $false $false $start.AddSeconds(31)) 'first failed cycle must not restart'
Assert-Equal $false (Update-SupervisorState $state $false $false $start.AddSeconds(36)) 'second failed cycle must not restart'
Assert-Equal $true (Update-SupervisorState $state $false $false $start.AddSeconds(41)) 'third failed cycle must request exactly one restart'
Assert-Equal 0 $state.FailureCount 'restart must reset shared failure counter'

Assert-Equal $false (Update-SupervisorState $state $false $true $start.AddSeconds(72)) 'first post-grace failure must not restart'
Assert-Equal $false (Update-SupervisorState $state $false $true $start.AddSeconds(77)) 'second post-grace failure must not restart'
Assert-Equal $false (Update-SupervisorState $state $false $true $start.AddSeconds(82)) 'cooldown must suppress repeated restart'
Assert-Equal 3 $state.FailureCount 'cooldown must retain saturated failure evidence'

Assert-Equal $true (Update-SupervisorState $state $false $true $start.AddSeconds(132)) 'persistent failure may restart after cooldown'
Assert-Equal $false (Update-SupervisorState $state $true $true $start.AddSeconds(163)) 'healthy cycle must not restart'
Assert-Equal 0 $state.FailureCount 'healthy cycle must clear failure evidence'

$keepaliveArguments = @(Get-WslKeepaliveArguments -Distro 'Ubuntu' `
  -User 'victor_1' `
  -KeepalivePath '/home/victor_1/.synon-biomed-v0.1.0/scripts/dev/source-wsl-keepalive.sh')
Assert-Equal 8 $keepaliveArguments.Count 'keepalive command must have one closed argv shape'
Assert-Equal '-d' $keepaliveArguments[0] 'keepalive must select an explicit distro'
Assert-Equal 'Ubuntu' $keepaliveArguments[1] 'keepalive must preserve the selected distro'
Assert-Equal '-u' $keepaliveArguments[2] 'keepalive must select an explicit launch user'
Assert-Equal 'root' $keepaliveArguments[3] 'keepalive bootstrap must run as root'
Assert-Equal '--exec' $keepaliveArguments[4] 'keepalive must not use a shell command string'
Assert-Equal '/usr/bin/env' $keepaliveArguments[5] 'keepalive must pass only one bounded environment value'
Assert-Equal 'SYNON_DEV_WSL_USER=victor_1' $keepaliveArguments[6] 'keepalive must bind the source-service user'

$invalidPathRejected = $false
try {
  $null = Get-WslKeepaliveArguments -Distro 'Ubuntu' -User 'victor_1' `
    -KeepalivePath 'relative/or/unsafe'
} catch {
  $invalidPathRejected = $true
}
Assert-Equal $true $invalidPathRejected 'keepalive path must be absolute and fail closed'

$abandonedMutex = [pscustomobject]@{}
$abandonedMutex | Add-Member -MemberType ScriptMethod -Name WaitOne -Value {
  param($MillisecondsTimeout, $ExitContext)
  throw [System.Threading.AbandonedMutexException]::new()
}
Assert-Equal $true (Enter-SupervisorMutex $abandonedMutex) 'abandoned watchdog mutex ownership must be recovered'

'source-dev-supervisor-test: ok'

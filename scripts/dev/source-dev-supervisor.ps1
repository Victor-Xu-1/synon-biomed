[CmdletBinding()]
param(
  [switch]$TestMode,
  [int]$FailureThreshold = 3,
  [int]$ProbeIntervalSeconds = 5,
  [int]$StartupGraceSeconds = 30,
  [int]$RestartCooldownSeconds = 90,
  [int]$RestartTimeoutSeconds = 45,
  [ValidatePattern('^[A-Za-z0-9_.-]+$')]
  [string]$WslDistro = 'Ubuntu',
  [ValidatePattern('^[A-Za-z0-9_.-]+$')]
  [string]$WslUser = 'victor_1',
  [ValidatePattern('^/[A-Za-z0-9_./-]+$')]
  [string]$WslKeepalivePath = '/home/victor_1/.synon-biomed-v0.1.0/scripts/dev/source-wsl-keepalive.sh',
  [string]$BackendHealthUri = 'http://127.0.0.1:8766/health',
  [string]$FrontendHealthUri = 'http://127.0.0.1:8765/'
)

$ErrorActionPreference = 'Stop'

function New-SupervisorState {
  param([datetime]$NowUtc)

  [pscustomobject]@{
    FailureCount = 0
    LastRestartUtc = [datetime]::MinValue
    GraceUntilUtc = $NowUtc.AddSeconds($StartupGraceSeconds)
  }
}

function Update-SupervisorState {
  param(
    [Parameter(Mandatory)]$State,
    [bool]$BackendHealthy,
    [bool]$FrontendHealthy,
    [datetime]$NowUtc
  )

  if ($BackendHealthy -and $FrontendHealthy) {
    $State.FailureCount = 0
    return $false
  }

  if ($NowUtc -lt $State.GraceUntilUtc) {
    return $false
  }

  $State.FailureCount += 1
  if ($State.FailureCount -lt $FailureThreshold) {
    return $false
  }

  if ($State.LastRestartUtc -ne [datetime]::MinValue -and
      $NowUtc -lt $State.LastRestartUtc.AddSeconds($RestartCooldownSeconds)) {
    $State.FailureCount = $FailureThreshold
    return $false
  }

  $State.FailureCount = 0
  $State.LastRestartUtc = $NowUtc
  $State.GraceUntilUtc = $NowUtc.AddSeconds($StartupGraceSeconds)
  return $true
}

function Get-WslKeepaliveArguments {
  param(
    [string]$Distro,
    [string]$User,
    [string]$KeepalivePath
  )

  if ([string]::IsNullOrWhiteSpace($Distro) -or
      [string]::IsNullOrWhiteSpace($User) -or
      [string]::IsNullOrWhiteSpace($KeepalivePath) -or
      -not $KeepalivePath.StartsWith('/')) {
    throw 'WSL distro, user, and absolute keepalive path are required.'
  }

  return @(
    '-d', $Distro, '-u', 'root', '--exec', '/usr/bin/env',
    "SYNON_DEV_WSL_USER=$User", $KeepalivePath
  )
}

function Enter-SupervisorMutex {
  param([Parameter(Mandatory)]$Mutex)

  try {
    return $Mutex.WaitOne(0, $false)
  } catch {
    $exception = $_.Exception
    while ($null -ne $exception) {
      if ($exception -is [System.Threading.AbandonedMutexException]) {
        # The previous watchdog may have been terminated by Task Scheduler.
        # .NET transfers ownership while reporting the abandonment.
        return $true
      }
      $exception = $exception.InnerException
    }
    throw
  }
}

if ($TestMode) {
  return
}

if ($FailureThreshold -lt 1 -or $ProbeIntervalSeconds -lt 1 -or
    $StartupGraceSeconds -lt 1 -or $RestartCooldownSeconds -lt 1 -or
    $RestartTimeoutSeconds -lt 1) {
  throw 'Supervisor timing values must be positive integers.'
}

$runtimeRoot = Join-Path $env:LOCALAPPDATA 'SynonBiomed\source-dev'
$logPath = Join-Path $runtimeRoot 'watchdog.log'
$oldLogPath = "$logPath.1"
$maxLogBytes = 512KB
New-Item -ItemType Directory -Path $runtimeRoot -Force | Out-Null

function Write-SupervisorLog {
  param([string]$Code, [string]$Detail = '')

  if (Test-Path -LiteralPath $logPath) {
    $item = Get-Item -LiteralPath $logPath -ErrorAction SilentlyContinue
    if ($item -and $item.Length -ge $maxLogBytes) {
      Move-Item -LiteralPath $logPath -Destination $oldLogPath -Force
    }
  }
  $timestamp = (Get-Date).ToUniversalTime().ToString('o')
  $suffix = if ($Detail) { " $Detail" } else { '' }
  Add-Content -LiteralPath $logPath -Value "$timestamp $Code$suffix"
}

function Test-Endpoint {
  param([string]$Uri)

  try {
    $response = Invoke-WebRequest -UseBasicParsing -Uri $Uri -TimeoutSec 3
    return $response.StatusCode -eq 200
  } catch {
    return $false
  }
}

function Start-WslKeepalive {
  $arguments = Get-WslKeepaliveArguments -Distro $WslDistro `
    -User $WslUser -KeepalivePath $WslKeepalivePath
  $process = Start-Process -FilePath 'wsl.exe' -ArgumentList $arguments `
    -WindowStyle Hidden -PassThru
  Write-SupervisorLog 'wsl_keepalive_started' "pid=$($process.Id)"
  return $process
}

function Stop-WslKeepalive {
  param($Process)

  if ($null -eq $Process -or $Process.HasExited) {
    return
  }
  Stop-Process -Id $Process.Id -Force -ErrorAction SilentlyContinue
  [void]$Process.WaitForExit(5000)
}

function Restart-SourceServices {
  $arguments = @(
    '-d', $WslDistro, '--exec', 'systemctl', '--user', 'restart',
    'synon-biomed-v010-backend.service',
    'synon-biomed-v010-frontend.service'
  )
  $process = Start-Process -FilePath 'wsl.exe' -ArgumentList $arguments `
    -WindowStyle Hidden -PassThru
  if (-not $process.WaitForExit($RestartTimeoutSeconds * 1000)) {
    Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    Write-SupervisorLog 'source_services_restart_timeout'
    return $false
  }
  if ($process.ExitCode -ne 0) {
    Write-SupervisorLog 'source_services_restart_failed' "exit=$($process.ExitCode)"
    return $false
  }
  Write-SupervisorLog 'source_services_restart_completed'
  return $true
}

$mutex = New-Object System.Threading.Mutex($false, 'Local\SynonBiomedSourceDevWatchdog')
$mutexAcquired = Enter-SupervisorMutex $mutex
if (-not $mutexAcquired) {
  $mutex.Dispose()
  exit 0
}

$state = New-SupervisorState -NowUtc ([datetime]::UtcNow)
$keepaliveProcess = $null
Write-SupervisorLog 'watchdog_started'
try {
  $keepaliveProcess = Start-WslKeepalive
  while ($true) {
    try {
      if ($keepaliveProcess.HasExited) {
        Write-SupervisorLog 'wsl_keepalive_exited' "exit=$($keepaliveProcess.ExitCode)"
        Start-Sleep -Seconds 2
        $keepaliveProcess = Start-WslKeepalive
        $state = New-SupervisorState -NowUtc ([datetime]::UtcNow)
      }
      $backendHealthy = Test-Endpoint $BackendHealthUri
      $frontendHealthy = Test-Endpoint $FrontendHealthUri
      $shouldRestart = Update-SupervisorState -State $state `
        -BackendHealthy $backendHealthy -FrontendHealthy $frontendHealthy `
        -NowUtc ([datetime]::UtcNow)
      if ($shouldRestart) {
        [void](Restart-SourceServices)
      }
    } catch {
      Write-SupervisorLog 'watchdog_loop_error' $_.Exception.GetType().Name
    }
    Start-Sleep -Seconds $ProbeIntervalSeconds
  }
} finally {
  Stop-WslKeepalive $keepaliveProcess
  $mutex.ReleaseMutex()
  $mutex.Dispose()
}

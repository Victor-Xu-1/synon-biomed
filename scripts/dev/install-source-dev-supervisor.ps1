[CmdletBinding()]
param(
  [switch]$Start,
  [ValidatePattern('^[A-Za-z0-9_.-]+$')]
  [string]$WslDistro = 'Ubuntu',
  [ValidatePattern('^[A-Za-z0-9_.-]+$')]
  [string]$WslUser = 'victor_1',
  [ValidatePattern('^/[A-Za-z0-9_./-]+$')]
  [string]$WslKeepalivePath = '/home/victor_1/.synon-biomed-v0.1.0/scripts/dev/source-wsl-keepalive.sh'
)

$ErrorActionPreference = 'Stop'
$taskName = 'Synon Biomed Source Dev Supervisor'
$sourceScript = Join-Path $PSScriptRoot 'source-dev-supervisor.ps1'
$runtimeRoot = Join-Path $env:LOCALAPPDATA 'SynonBiomed\source-dev'
$runtimeScript = Join-Path $runtimeRoot 'watchdog.ps1'

if (-not (Test-Path -LiteralPath $sourceScript)) {
  throw "Missing canonical supervisor: $sourceScript"
}

New-Item -ItemType Directory -Path $runtimeRoot -Force | Out-Null
Copy-Item -LiteralPath $sourceScript -Destination $runtimeScript -Force

$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument `
  "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$runtimeScript`" -WslDistro `"$WslDistro`" -WslUser `"$WslUser`" -WslKeepalivePath `"$WslKeepalivePath`""
$trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries `
  -DontStopIfGoingOnBatteries -StartWhenAvailable `
  -MultipleInstances IgnoreNew -ExecutionTimeLimit ([timespan]::Zero) `
  -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1)
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" `
  -LogonType Interactive -RunLevel Limited

Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
  -Settings $settings -Principal $principal -Force | Out-Null

if ($Start) {
  Start-ScheduledTask -TaskName $taskName
}

$sourceHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceScript).Hash
$runtimeHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $runtimeScript).Hash
if ($sourceHash -ne $runtimeHash) {
  throw 'Installed supervisor does not match canonical source.'
}

[pscustomobject]@{
  TaskName = $taskName
  RuntimeScript = $runtimeScript
  SHA256 = $runtimeHash
  WslDistro = $WslDistro
  WslUser = $WslUser
  WslKeepalivePath = $WslKeepalivePath
  Started = [bool]$Start
}

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Archive,
  [Parameter(Mandatory = $true)][string]$RepoRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http
. (Join-Path $PSScriptRoot 'release-test-environment.ps1')
$root = Join-Path ([System.IO.Path]::GetTempPath()) ('synon-go-windows-release-test-' + [Guid]::NewGuid().ToString('N'))
$install = Join-Path $root 'install'
$backup = Join-Path $root 'backup'
$data = Join-Path $root 'user-data'
$installer = Join-Path $RepoRoot 'scripts\install-release.ps1'
$manager = Join-Path $RepoRoot 'scripts\manage-release.ps1'
$passed = $false
$junction = $null
$managerJunction = $null

function Get-TestPathEntries {
  param([Parameter(Mandatory = $true)][string]$Path)

  $entryParent = Split-Path -Parent $Path
  $entryName = Split-Path -Leaf $Path
  if ([string]::IsNullOrWhiteSpace($entryParent) -or -not (Test-Path -LiteralPath $entryParent -PathType Container)) {
    return
  }
  Get-ChildItem -LiteralPath $entryParent -Force -ErrorAction Stop |
    Where-Object { [string]::Equals($_.Name, $entryName, [System.StringComparison]::OrdinalIgnoreCase) }
}

function Remove-TestReparsePoint {
  param(
    [Parameter(Mandatory = $true)][string]$Root,
    [Parameter(Mandatory = $true)][string]$Path
  )

  $rootFull = [System.IO.Path]::GetFullPath($Root).TrimEnd('\', '/')
  $pathFull = [System.IO.Path]::GetFullPath($Path)
  $pathParent = (Split-Path -Parent $pathFull).TrimEnd('\', '/')
  if (-not [string]::Equals($pathParent, $rootFull, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Refusing to unlink a test reparse point outside the owned cleanup root'
  }
  $items = @(Get-TestPathEntries -Path $pathFull)
  if ($items.Count -ne 1) {
    throw "Expected exactly one test reparse point entry: $pathFull"
  }
  $item = $items[0]
  if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -eq 0) {
    throw "Refusing to unlink a non-reparse test path: $pathFull"
  }
  if (($item.Attributes -band [System.IO.FileAttributes]::Directory) -eq 0) {
    throw "Refusing to unlink a non-directory test reparse point: $pathFull"
  }

  # Windows PowerShell 5.1's FileSystem provider can throw a
  # NullReferenceException when Remove-Item unlinks a dangling or cyclic
  # junction. Directory.Delete with recursive=false removes only the junction
  # entry and never traverses its target.
  [System.IO.Directory]::Delete($pathFull, $false)
  if (@(Get-TestPathEntries -Path $pathFull).Count -ne 0) {
    throw "Windows release test could not unlink reparse point: $pathFull"
  }
}

function Test-NativeRuntimeWithoutLanguageTools {
  param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Root
  )

  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $listener.Start()
  $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
  $listener.Stop()
  $runtimeHome = Join-Path $Root 'native-runtime-home'
  $emptyPath = Join-Path $Root 'empty-runtime-path'
  New-Item -ItemType Directory -Force -Path $runtimeHome, $emptyPath | Out-Null
  $stdout = Join-Path $Root 'native-runtime.stdout.log'
  $stderr = Join-Path $Root 'native-runtime.stderr.log'
  $saved = @{
    PATH = $env:PATH
    SYNON_HOME = $env:SYNON_HOME
    SYNON_ADDRESS = $env:SYNON_ADDRESS
    SYNON_RUNNER_ENABLED = $env:SYNON_RUNNER_ENABLED
    SYNON_ENABLED_ADAPTERS = $env:SYNON_ENABLED_ADAPTERS
  }
  $process = $null
  try {
    $env:PATH = $emptyPath
    $env:SYNON_HOME = $runtimeHome
    $env:SYNON_ADDRESS = "127.0.0.1:$port"
    $env:SYNON_RUNNER_ENABLED = 'false'
    $env:SYNON_ENABLED_ADAPTERS = ''
    $process = Start-Process -FilePath $Binary -ArgumentList 'serve' -PassThru -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr
  } finally {
    foreach ($key in $saved.Keys) {
      if ($null -eq $saved[$key]) {
        Remove-Item "Env:$key" -ErrorAction SilentlyContinue
      } else {
        Set-Item "Env:$key" $saved[$key]
      }
    }
  }

  try {
    $ready = Wait-SynonReleaseHTTPReady -BaseUri "http://127.0.0.1:$port" -HasExited { $process.HasExited }
    if (-not $ready) {
      $details = ''
      if (Test-Path -LiteralPath $stderr) { $details = Get-Content -LiteralPath $stderr -Raw }
      throw "Installed Windows runtime did not start without language runtimes: $details"
    }
  } finally {
    if ($null -ne $process -and -not $process.HasExited) {
      Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
      $process.WaitForExit()
    }
  }
}

Invoke-SynonReleaseTestEnvironment -Root $root -Action {
try {
  New-Item -ItemType Directory -Force -Path $data | Out-Null
  Set-Content -LiteralPath (Join-Path $data 'marker') -Value 'preserve-me' -NoNewline

  $homeRejected = $false
  try { & $installer -Archive $Archive -InstallDir ([Environment]::GetFolderPath('UserProfile')) -NoRunCheck } catch {
    if ($_.Exception.Message -notmatch 'unsafe install directory') { throw }
    $homeRejected = $true
  }
  if (-not $homeRejected) { throw 'Installer accepted the user profile as an install target' }

  $junctionTarget = Join-Path $root 'junction-target'
  $junction = Join-Path $root 'junction'
  New-Item -ItemType Directory -Force -Path $junctionTarget | Out-Null
  New-Item -ItemType Junction -Path $junction -Target $junctionTarget | Out-Null
  $junctionRejected = $false
  try { & $installer -Archive $Archive -InstallDir (Join-Path $junction 'install') -NoRunCheck } catch {
    if ($_.Exception.Message -notmatch 'reparse point') { throw }
    $junctionRejected = $true
  }
  if (-not $junctionRejected -or (Test-Path -LiteralPath (Join-Path $junctionTarget 'install'))) {
    throw 'Installer accepted a path through a junction'
  }
  Remove-Item -LiteralPath $junctionTarget -Recurse -Force
  $danglingRejected = $false
  try { & $installer -Archive $Archive -InstallDir (Join-Path $junction 'install') -NoRunCheck } catch {
    if ($_.Exception.Message -notmatch 'reparse point') { throw }
    $danglingRejected = $true
  }
  if (-not $danglingRejected) { throw 'Installer accepted a path through a dangling junction' }

  & $installer -Archive $Archive -InstallDir $install
  $manager = Join-Path $install 'scripts\manage-release.ps1'

  $managerJunction = Join-Path $root 'manager-junction'
  New-Item -ItemType Junction -Path $managerJunction -Target $root | Out-Null
  $managerRejected = $false
  try { & $manager -Action Uninstall -InstallDir (Join-Path $managerJunction 'install') -AllowCorrupt } catch {
    if ($_.Exception.Message -notmatch 'reparse point') { throw }
    $managerRejected = $true
  }
  if (-not $managerRejected -or -not (Test-Path -LiteralPath $install)) {
    throw 'Release manager accepted an install path through a junction'
  }
  $backupJunctionRejected = $false
  try { & $manager -Action Backup -InstallDir $install -BackupDir (Join-Path $managerJunction 'unsafe-backup') } catch {
    if ($_.Exception.Message -notmatch 'reparse point') { throw }
    $backupJunctionRejected = $true
  }
  if (-not $backupJunctionRejected -or (Test-Path -LiteralPath (Join-Path $root 'unsafe-backup'))) {
    throw 'Release manager accepted a backup path through a junction'
  }

  & $manager -Action Backup -InstallDir $install -BackupDir $backup
  $legacyBackup = Join-Path $root 'legacy-backup'
  Copy-Item -LiteralPath $backup -Destination $legacyBackup -Recurse
  $legacyIdentityPath = Join-Path $legacyBackup 'product-identity.json'
  $legacyIdentity = (Get-Content -LiteralPath $legacyIdentityPath -Raw) | ConvertFrom-Json
  $legacyIdentity.version = '4.0.2'
  $legacyIdentity | ConvertTo-Json | Set-Content -LiteralPath $legacyIdentityPath -Encoding UTF8
  $installedSha = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $install 'synon-go.exe')).Hash
  $legacyRejected = $false
  try { & $manager -Action Rollback -InstallDir $install -BackupDir $legacyBackup } catch {
    if ($_.Exception.Message -ne 'Release product identity does not match the trusted authority') { throw }
    $legacyRejected = $true
  }
  if (-not $legacyRejected -or $installedSha -ne (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $install 'synon-go.exe')).Hash) {
    throw 'Release manager accepted or mutated state for a legacy product identity'
  }
  Add-Content -LiteralPath (Join-Path $install 'README.md') -Value 'corrupt'
  & $manager -Action Rollback -InstallDir $install -BackupDir $backup
  $health = (& (Join-Path $install 'synon-go.exe') --health-json | ConvertFrom-Json)
  $identity = (Get-Content -LiteralPath (Join-Path $install 'product-identity.json') -Raw) | ConvertFrom-Json
  if ($health.name -ne $identity.display_name -or $health.version -ne $identity.version) { throw 'Unexpected installed identity' }
  & (Join-Path $install 'synon-go.exe') tui --help *> $null
  if ($LASTEXITCODE -eq 0) { throw 'Retired TUI command is still available' }
  Test-NativeRuntimeWithoutLanguageTools -Binary (Join-Path $install 'synon-go.exe') -Root $root
  $previousSynonHome = $env:SYNON_HOME
  $previousRunnerEnabled = $env:SYNON_RUNNER_ENABLED
  $previousRunnerProvider = $env:SYNON_RUNNER_PROVIDER
  try {
    $env:SYNON_HOME = Join-Path $root 'model-smoke-home'
    $modelPlanRaw = & (Join-Path $install 'synon-go.exe') model-smoke --target runner --plan --require-all --json
    if ($LASTEXITCODE -ne 0) { throw "Model smoke plan exited with code $LASTEXITCODE" }
    $modelPlan = $modelPlanRaw | ConvertFrom-Json
    if (-not $modelPlan.secretsRedacted -or $modelPlan.mode -ne 'plan') { throw 'Model smoke plan was not redacted' }
    $runnerPlan = $modelPlan.targets | Where-Object { $_.name -eq 'runner' } | Select-Object -First 1
    if ($null -eq $runnerPlan -or $runnerPlan.provider -ne 'workspace' -or $runnerPlan.status -ne 'skipped_missing_config' -or $runnerPlan.configured) { throw 'Default workspace model authority did not fail closed' }
    $env:SYNON_RUNNER_ENABLED = 'true'
    $env:SYNON_RUNNER_PROVIDER = 'go_builtin'
    $modelRunRaw = & (Join-Path $install 'synon-go.exe') model-smoke --target runner --run --require-all --json
    if ($LASTEXITCODE -ne 0) { throw "Built-in runner model smoke exited with code $LASTEXITCODE" }
    $modelRun = $modelRunRaw | ConvertFrom-Json
    if ($modelRun.status -ne 'passed' -or -not $modelRun.secretsRedacted) { throw 'Built-in runner model smoke failed' }
  } finally {
    if ($null -eq $previousSynonHome) {
      Remove-Item Env:SYNON_HOME -ErrorAction SilentlyContinue
    } else {
      $env:SYNON_HOME = $previousSynonHome
    }
    if ($null -eq $previousRunnerEnabled) {
      Remove-Item Env:SYNON_RUNNER_ENABLED -ErrorAction SilentlyContinue
    } else {
      $env:SYNON_RUNNER_ENABLED = $previousRunnerEnabled
    }
    if ($null -eq $previousRunnerProvider) {
      Remove-Item Env:SYNON_RUNNER_PROVIDER -ErrorAction SilentlyContinue
    } else {
      $env:SYNON_RUNNER_PROVIDER = $previousRunnerProvider
    }
  }
  & $installer -Archive $Archive -InstallDir $install
  $manager = Join-Path $install 'scripts\manage-release.ps1'
  Add-Content -LiteralPath (Join-Path $install 'README.md') -Value 'corrupt'
  $rejected = $false
  try { & $manager -Action Uninstall -InstallDir $install } catch { $rejected = $true }
  if (-not $rejected -or -not (Test-Path -LiteralPath $install)) { throw 'Corrupt uninstall was not rejected' }
  & $manager -Action Uninstall -InstallDir $install -AllowCorrupt
  if (Test-Path -LiteralPath $install) { throw 'Uninstall left the install directory behind' }
  if ((Get-Content -LiteralPath (Join-Path $data 'marker') -Raw) -ne 'preserve-me') { throw 'External data changed' }
  $passed = $true
} finally {
  if ($passed) {
    try {
      foreach ($link in @($managerJunction, $junction)) {
        if ($null -eq $link) { continue }
        Remove-TestReparsePoint -Root $root -Path $link
      }
      if (-not (Test-Path -LiteralPath $root -PathType Container)) {
        throw 'Windows release test cleanup root disappeared before owned cleanup'
      }
      if ((Get-Content -LiteralPath (Join-Path $data 'marker') -Raw) -ne 'preserve-me') {
        throw 'External data changed during junction cleanup'
      }
      $remainingReparsePoints = @(Get-ChildItem -LiteralPath $root -Force | Where-Object {
        ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0
      })
      if ($remainingReparsePoints.Count -ne 0) { throw 'Windows release test left a reparse point in its cleanup root' }
      Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction Stop
      if (Test-Path -LiteralPath $root) { throw 'Windows release test cleanup root still exists' }
    } catch {
      Write-Warning "Windows release test cleanup failed; evidence preserved at $root"
      throw
    }
  } else {
    Write-Warning "Windows release test evidence preserved at $root"
  }
}
if ($passed) { Write-Output 'WINDOWS_RELEASE_TEST=yes' }
}

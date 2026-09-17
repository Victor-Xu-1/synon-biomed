[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Archive,

  [Parameter(Mandatory = $true)]
  [string]$InstallDir,

  [switch]$NoRunCheck
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot 'release-identity-policy.ps1')
$script:TrustedIdentityPath = Join-Path $PSScriptRoot '..\product-identity.json'

function Test-ArchiveEntry {
  param([Parameter(Mandatory = $true)][string]$Entry)

  $normalized = $Entry.Trim().Replace('\', '/')
  if ([string]::IsNullOrWhiteSpace($normalized) -or
      $normalized.StartsWith('/') -or
      $normalized -match '^[A-Za-z]:' -or
      $normalized.Contains([char]0)) {
    return $false
  }
  foreach ($segment in $normalized.Split('/')) {
    if ($segment -eq '..') {
      return $false
    }
  }
  return $true
}

function Get-PackageBinary {
  param([Parameter(Mandatory = $true)][string]$Root)

  foreach ($name in @('synon-go.exe', 'synon-go')) {
    $candidate = Join-Path $Root $name
    if (Test-Path -LiteralPath $candidate -PathType Leaf) {
      return $candidate
    }
  }
  throw "Package does not contain a runnable synon-go binary"
}

function Assert-SafeReleasePath {
  param([Parameter(Mandatory = $true)][string]$Path)

  if ([string]::IsNullOrWhiteSpace($Path)) {
    throw "Refusing an empty install directory"
  }
  $full = [System.IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
  $root = [System.IO.Path]::GetPathRoot($full).TrimEnd('\', '/')
  $userHome = [Environment]::GetFolderPath('UserProfile').TrimEnd('\', '/')
  if ([string]::Equals($full, $root, [System.StringComparison]::OrdinalIgnoreCase) -or
      [string]::Equals($full, $userHome, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing unsafe install directory: $full"
  }
  $cursor = $full
  while (-not [string]::IsNullOrWhiteSpace($cursor) -and
         -not [string]::Equals($cursor, $root, [System.StringComparison]::OrdinalIgnoreCase)) {
    $item = Get-Item -LiteralPath $cursor -Force -ErrorAction SilentlyContinue
    if ($null -eq $item) {
      $entryParent = Split-Path -Parent $cursor
      $entryName = Split-Path -Leaf $cursor
      if (-not [string]::IsNullOrWhiteSpace($entryParent) -and (Test-Path -LiteralPath $entryParent -PathType Container)) {
        $item = Get-ChildItem -LiteralPath $entryParent -Force -ErrorAction SilentlyContinue |
          Where-Object { [string]::Equals($_.Name, $entryName, [System.StringComparison]::OrdinalIgnoreCase) } |
          Select-Object -First 1
      }
    }
    if ($null -ne $item -and
        ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "Refusing release path through a reparse point: $cursor"
    }
    $parent = Split-Path -Parent $cursor
    if ([string]::Equals($parent, $cursor, [System.StringComparison]::OrdinalIgnoreCase)) { break }
    $cursor = $parent
  }
  return $full
}

function Assert-ReleasePathUnchanged {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Expected
  )

  $current = Assert-SafeReleasePath -Path $Path
  if (-not [string]::Equals($current, $Expected, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Release path changed during the operation"
  }
}

function Assert-ManagedReleaseChild {
  param(
    [Parameter(Mandatory = $true)][string]$Parent,
    [Parameter(Mandatory = $true)][string]$Path
  )

  $parentFull = [System.IO.Path]::GetFullPath($Parent).TrimEnd('\', '/')
  $childFull = Assert-SafeReleasePath -Path $Path
  $childParent = (Split-Path -Parent $childFull).TrimEnd('\', '/')
  if (-not [string]::Equals($childParent, $parentFull, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing a generated release path outside its managed parent"
  }
  return $childFull
}

function Assert-ReleaseValid {
  param(
    [Parameter(Mandatory = $true)][string]$Root,
    [switch]$CheckRun
  )

  $identity = Assert-SynonProductIdentity -TrustedIdentityPath $script:TrustedIdentityPath -CandidateRoot $Root
  $binary = Get-PackageBinary -Root $Root
  $verificationRaw = @(& $binary release-manifest verify --root $Root 2>&1)
  if ($LASTEXITCODE -ne 0) {
    throw "Release manifest verification failed: $($verificationRaw -join [Environment]::NewLine)"
  }
  $verification = ($verificationRaw -join [Environment]::NewLine) | ConvertFrom-Json
  if ($verification.valid -ne $true) {
    throw "Release manifest verification did not return valid=true"
  }
  if ($CheckRun) {
    $healthRaw = @(& $binary serve --health-json 2>&1)
    if ($LASTEXITCODE -ne 0) {
      throw "$($identity.display_name) health check failed: $($healthRaw -join [Environment]::NewLine)"
    }
    $health = ($healthRaw -join [Environment]::NewLine) | ConvertFrom-Json
    if ($health.status -ne 'ok' -or
        $health.name -ne $identity.display_name -or
        $health.version -ne $identity.version) {
      throw "$($identity.display_name) health check returned an unexpected response"
    }
  }
}

if (-not (Test-Path -LiteralPath $Archive -PathType Leaf)) {
  throw "Archive not found: $Archive"
}
$archivePath = (Resolve-Path -LiteralPath $Archive).ProviderPath
$installPath = Assert-SafeReleasePath -Path $InstallDir

$entries = @(& tar -tzf $archivePath 2>&1)
if ($LASTEXITCODE -ne 0) {
  throw "Unable to list archive: $($entries -join [Environment]::NewLine)"
}
foreach ($entry in $entries) {
  if (-not (Test-ArchiveEntry -Entry $entry)) {
    throw "Archive contains an unsafe path: $entry"
  }
}
$entryDetails = @(& tar -tvzf $archivePath 2>&1)
if ($LASTEXITCODE -ne 0 -or $entryDetails.Count -ne $entries.Count) {
  throw "Unable to inspect release archive entry types"
}
foreach ($detail in $entryDetails) {
  if ([string]::IsNullOrWhiteSpace($detail) -or $detail[0] -notin @('-', 'd')) {
    throw "Archive contains a non-regular entry: $detail"
  }
}

$temp = Join-Path ([System.IO.Path]::GetTempPath()) ("synon-go-install-" + [System.Guid]::NewGuid().ToString("N"))
$parent = Split-Path -Parent $installPath
$name = Split-Path -Leaf $installPath
$stage = Join-Path $parent (".$name.stage." + [System.Guid]::NewGuid().ToString("N"))
$backup = $null
$installed = $false
New-Item -ItemType Directory -Force -Path $temp | Out-Null
New-Item -ItemType Directory -Force -Path $parent | Out-Null
Assert-ReleasePathUnchanged -Path $InstallDir -Expected $installPath

try {
  & tar -xzf $archivePath -C $temp
  if ($LASTEXITCODE -ne 0) {
    throw "Unable to extract release archive"
  }
  $topLevel = @(Get-ChildItem -LiteralPath $temp -Force)
  if ($topLevel.Count -ne 1 -or -not $topLevel[0].PSIsContainer) {
    throw "Archive must contain exactly one package directory"
  }
  $packageDir = $topLevel[0].FullName
  Assert-ReleaseValid -Root $packageDir -CheckRun:(-not $NoRunCheck)

  Assert-ManagedReleaseChild -Parent $parent -Path $stage | Out-Null
  New-Item -ItemType Directory -Path $stage | Out-Null
  Assert-ManagedReleaseChild -Parent $parent -Path $stage | Out-Null
  Get-ChildItem -LiteralPath $packageDir -Force | Copy-Item -Destination $stage -Recurse -Force
  Assert-ReleaseValid -Root $stage

  if (Test-Path -LiteralPath $installPath) {
    Assert-ReleasePathUnchanged -Path $InstallDir -Expected $installPath
    $backup = Join-Path $parent (".$name.backup." + [System.Guid]::NewGuid().ToString("N"))
    Assert-ManagedReleaseChild -Parent $parent -Path $backup | Out-Null
    Move-Item -LiteralPath $installPath -Destination $backup
  }
  Assert-ReleasePathUnchanged -Path $InstallDir -Expected $installPath
  Assert-ManagedReleaseChild -Parent $parent -Path $stage | Out-Null
  Move-Item -LiteralPath $stage -Destination $installPath
  $installed = $true
  Assert-ReleaseValid -Root $installPath -CheckRun:(-not $NoRunCheck)

  if ($null -ne $backup -and (Test-Path -LiteralPath $backup)) {
    Assert-ReleasePathUnchanged -Path $InstallDir -Expected $installPath
    Assert-ManagedReleaseChild -Parent $parent -Path $backup | Out-Null
    Remove-Item -LiteralPath $backup -Recurse -Force
    $backup = $null
  }
  Write-Output "synon-go installed to $installPath"
}
catch {
  $originalError = $_
  if ($installed -and (Test-Path -LiteralPath $installPath)) {
    try {
      Assert-ReleasePathUnchanged -Path $InstallDir -Expected $installPath
      Remove-Item -LiteralPath $installPath -Recurse -Force
    } catch {
      Write-Warning "Unable to remove the failed install safely; manual recovery is required at $installPath"
    }
  }
  if ($null -ne $backup -and (Test-Path -LiteralPath $backup)) {
    try {
      Assert-ReleasePathUnchanged -Path $InstallDir -Expected $installPath
      Assert-ManagedReleaseChild -Parent $parent -Path $backup | Out-Null
      if (Test-Path -LiteralPath $installPath) { throw "Install target is not empty" }
      Move-Item -LiteralPath $backup -Destination $installPath
      $backup = $null
    } catch {
      Write-Warning "Previous installation preserved for manual recovery at $backup"
    }
  }
  throw $originalError
}
finally {
  if (Test-Path -LiteralPath $temp) {
    Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
  }
  if (Test-Path -LiteralPath $stage) {
    try {
      Assert-ManagedReleaseChild -Parent $parent -Path $stage | Out-Null
      Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction Stop
    } catch {
      Write-Warning "Install stage preserved for manual recovery at $stage"
    }
  }
}

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [ValidateSet('Backup', 'Rollback', 'Uninstall')]
  [string]$Action,
  [Parameter(Mandatory = $true)]
  [string]$InstallDir,
  [string]$BackupDir,
  [switch]$AllowCorrupt
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'release-identity-policy.ps1')
$script:TrustedIdentityPath = Join-Path $PSScriptRoot '..\product-identity.json'

function Resolve-SafePath([string]$Path) {
  if ([string]::IsNullOrWhiteSpace($Path)) { throw 'Refusing an empty release path' }
  $full = [System.IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
  $root = [System.IO.Path]::GetPathRoot($full).TrimEnd('\', '/')
  $userHome = [Environment]::GetFolderPath('UserProfile').TrimEnd('\', '/')
  if ([string]::Equals($full, $root, [System.StringComparison]::OrdinalIgnoreCase) -or
      [string]::Equals($full, $userHome, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing unsafe release path: $full"
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

function Assert-ReleasePathUnchanged([string]$Path, [string]$Expected) {
  $current = Resolve-SafePath $Path
  if (-not [string]::Equals($current, $Expected, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Release path changed during the operation'
  }
}

function Assert-ManagedReleaseChild([string]$Parent, [string]$Path) {
  $parentFull = [System.IO.Path]::GetFullPath($Parent).TrimEnd('\', '/')
  $childFull = Resolve-SafePath $Path
  $childParent = (Split-Path -Parent $childFull).TrimEnd('\', '/')
  if (-not [string]::Equals($childParent, $parentFull, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Refusing a generated release path outside its managed parent'
  }
  return $childFull
}

function Get-ReleaseBinary([string]$Root) {
  foreach ($name in @('synon-go.exe', 'synon-go')) {
    $candidate = Join-Path $Root $name
    if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
  }
  throw "Release binary is missing from $Root"
}

function Assert-Release([string]$Root) {
  if (-not (Test-Path -LiteralPath (Join-Path $Root 'RELEASE_MANIFEST.json') -PathType Leaf)) {
    throw "Not a verified product release: $Root"
  }
  [void](Assert-SynonProductIdentity -TrustedIdentityPath $script:TrustedIdentityPath -CandidateRoot $Root)
  $binary = Get-ReleaseBinary $Root
  & $binary release-manifest verify --root $Root | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Release validation failed: $Root" }
}

$install = Resolve-SafePath $InstallDir
switch ($Action) {
  'Backup' {
    if ([string]::IsNullOrWhiteSpace($BackupDir)) { throw 'BackupDir is required for Backup' }
    $backup = Resolve-SafePath $BackupDir
    Assert-Release $install
    if (Test-Path -LiteralPath $backup) { throw "Backup path already exists: $backup" }
    $parent = Split-Path -Parent $backup
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
    Assert-ReleasePathUnchanged $InstallDir $install
    Assert-ReleasePathUnchanged $BackupDir $backup
    $stage = Join-Path $parent ('.synon-go-backup-stage-' + [Guid]::NewGuid().ToString('N'))
    try {
      Assert-ManagedReleaseChild $parent $stage | Out-Null
      Copy-Item -LiteralPath $install -Destination $stage -Recurse
      Assert-ManagedReleaseChild $parent $stage | Out-Null
      Assert-Release $stage
      Assert-ReleasePathUnchanged $BackupDir $backup
      Assert-ManagedReleaseChild $parent $stage | Out-Null
      Move-Item -LiteralPath $stage -Destination $backup
    } finally {
      if (Test-Path -LiteralPath $stage) {
        Assert-ManagedReleaseChild $parent $stage | Out-Null
        Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction Stop
      }
    }
    Write-Output "synon-go release backed up to $backup"
  }
  'Rollback' {
    if ([string]::IsNullOrWhiteSpace($BackupDir)) { throw 'BackupDir is required for Rollback' }
    $backup = Resolve-SafePath $BackupDir
    Assert-Release $backup
    $parent = Split-Path -Parent $install
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
    Assert-ReleasePathUnchanged $InstallDir $install
    Assert-ReleasePathUnchanged $BackupDir $backup
    $stage = Join-Path $parent ('.synon-go-rollback-stage-' + [Guid]::NewGuid().ToString('N'))
    $previous = $null
    $rollbackSucceeded = $false
    try {
      Assert-ManagedReleaseChild $parent $stage | Out-Null
      Copy-Item -LiteralPath $backup -Destination $stage -Recurse
      Assert-ManagedReleaseChild $parent $stage | Out-Null
      Assert-Release $stage
      if (Test-Path -LiteralPath $install) {
        Assert-ReleasePathUnchanged $InstallDir $install
        $previous = Join-Path $parent ('.synon-go-pre-rollback-' + [Guid]::NewGuid().ToString('N'))
        Assert-ManagedReleaseChild $parent $previous | Out-Null
        Move-Item -LiteralPath $install -Destination $previous
      }
      Assert-ReleasePathUnchanged $InstallDir $install
      Assert-ManagedReleaseChild $parent $stage | Out-Null
      Move-Item -LiteralPath $stage -Destination $install
      Assert-Release $install
      $rollbackSucceeded = $true
    } catch {
      $originalError = $_
      if ($null -ne $previous -and (Test-Path -LiteralPath $previous)) {
        try {
          if (Test-Path -LiteralPath $install) {
            Assert-ReleasePathUnchanged $InstallDir $install
            Remove-Item -LiteralPath $install -Recurse -Force -ErrorAction Stop
          }
          Assert-ReleasePathUnchanged $InstallDir $install
          Assert-ManagedReleaseChild $parent $previous | Out-Null
          Move-Item -LiteralPath $previous -Destination $install
          $previous = $null
        } catch {
          Write-Warning "Previous installation preserved for manual recovery at $previous"
        }
      }
      throw $originalError
    } finally {
      if (Test-Path -LiteralPath $stage) {
        Assert-ManagedReleaseChild $parent $stage | Out-Null
        Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction Stop
      }
      if ($rollbackSucceeded -and $null -ne $previous -and (Test-Path -LiteralPath $previous)) {
        Assert-ManagedReleaseChild $parent $previous | Out-Null
        Remove-Item -LiteralPath $previous -Recurse -Force -ErrorAction Stop
        $previous = $null
      } elseif ($null -ne $previous -and (Test-Path -LiteralPath $previous)) {
        Write-Warning "Previous installation preserved for manual recovery at $previous"
      }
    }
    Write-Output "synon-go rolled back from $backup to $install"
  }
  'Uninstall' {
    if (-not (Test-Path -LiteralPath (Join-Path $install 'RELEASE_MANIFEST.json') -PathType Leaf)) {
      throw "Not a verified product release: $install"
    }
    if (-not $AllowCorrupt) { Assert-Release $install }
    Assert-ReleasePathUnchanged $InstallDir $install
    Remove-Item -LiteralPath $install -Recurse -Force
    Write-Output "synon-go uninstalled from $install; external data directories were preserved"
  }
}

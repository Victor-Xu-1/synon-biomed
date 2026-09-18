Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$root = Join-Path ([System.IO.Path]::GetTempPath()) ('synon-release-identity-test-' + [Guid]::NewGuid().ToString('N'))
$trusted = Join-Path $root 'trusted.json'
$current = Join-Path $root 'current'
$legacy = Join-Path $root 'legacy'
$passed = $false
$junction = $null
try {
  New-Item -ItemType Directory -Force -Path $current, $legacy | Out-Null
  Copy-Item -LiteralPath (Join-Path $repoRoot 'product-identity.json') -Destination $trusted
  Copy-Item -LiteralPath $trusted -Destination (Join-Path $current 'product-identity.json')
  $legacyIdentity = (Get-Content -LiteralPath $trusted -Raw) | ConvertFrom-Json
  $legacyIdentity.version = '4.0.2'
  $legacyIdentity | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $legacy 'product-identity.json') -Encoding UTF8

  . (Join-Path $PSScriptRoot 'release-identity-policy.ps1')
  $identity = Assert-SynonProductIdentity -TrustedIdentityPath $trusted -CandidateRoot $current
  $expectedIdentity = (Get-Content -LiteralPath $trusted -Raw) | ConvertFrom-Json
  if ($identity.display_name -ne 'Synon Biomed' -or $identity.version -ne $expectedIdentity.version) {
    throw 'Trusted identity projection is incorrect'
  }
  $rejected = $false
  try { [void](Assert-SynonProductIdentity -TrustedIdentityPath $trusted -CandidateRoot $legacy) } catch {
    if ($_.Exception.Message -ne 'Release product identity does not match the trusted authority') { throw }
    $rejected = $true
  }
  if (-not $rejected) { throw 'Identity policy accepted a legacy product version' }

  $junctionTarget = Join-Path $root 'junction-target'
  $junction = Join-Path $root 'junction'
  New-Item -ItemType Directory -Force -Path $junctionTarget | Out-Null
  Copy-Item -LiteralPath $trusted -Destination (Join-Path $junctionTarget 'trusted.json')
  New-Item -ItemType Junction -Path $junction -Target $junctionTarget | Out-Null
  $reparseRejected = $false
  try { [void](Assert-SynonProductIdentity -TrustedIdentityPath (Join-Path $junction 'trusted.json') -CandidateRoot $current) } catch {
    if ($_.Exception.Message -ne 'Trusted product identity path must not traverse a reparse point') { throw }
    $reparseRejected = $true
  }
  if (-not $reparseRejected) { throw 'Identity policy accepted a reparse-point trust root' }
  $passed = $true
} finally {
  if ($null -ne $junction -and (Test-Path -LiteralPath $junction)) {
    [System.IO.Directory]::Delete($junction, $false)
  }
  if ($passed -and (Test-Path -LiteralPath $root -PathType Container)) {
    Remove-Item -LiteralPath $root -Recurse -Force
  } elseif (-not $passed) {
    Write-Warning "Release identity test evidence preserved at $root"
  }
}
if ($passed) { Write-Output 'RELEASE_IDENTITY_POLICY_TEST=yes' }

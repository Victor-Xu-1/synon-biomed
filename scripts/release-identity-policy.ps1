function Read-SynonProductIdentity {
  param([Parameter(Mandatory = $true)][string]$Path)

  $full = [System.IO.Path]::GetFullPath($Path)
  $item = Get-Item -LiteralPath $full -Force -ErrorAction Stop
  if ($item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw 'Product identity authority must be a regular file'
  }
  $value = (Get-Content -LiteralPath $full -Raw -ErrorAction Stop) | ConvertFrom-Json
  $properties = @($value.PSObject.Properties.Name | Sort-Object)
  if (($properties -join ',') -ne 'display_name,machine_slug,schema,version' -or
      $value.schema -ne 'synon.product-identity.v1' -or
      [string]::IsNullOrWhiteSpace($value.display_name) -or
      $value.version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' -or
      $value.machine_slug -notmatch '^[a-z0-9]+(?:-[a-z0-9]+)*$') {
    throw 'Product identity authority is invalid'
  }
  return $value
}

function Resolve-SynonTrustedIdentityPath {
  param([Parameter(Mandatory = $true)][string]$Path)

  $full = [System.IO.Path]::GetFullPath($Path)
  $root = [System.IO.Path]::GetPathRoot($full).TrimEnd('\', '/')
  $cursor = $full
  while (-not [string]::IsNullOrWhiteSpace($cursor)) {
    $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop
    if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw 'Trusted product identity path must not traverse a reparse point'
    }
    if ([string]::Equals($cursor.TrimEnd('\', '/'), $root, [System.StringComparison]::OrdinalIgnoreCase)) {
      break
    }
    $parent = Split-Path -Parent $cursor
    if ([string]::IsNullOrWhiteSpace($parent) -or [string]::Equals($parent, $cursor, [System.StringComparison]::OrdinalIgnoreCase)) {
      break
    }
    $cursor = $parent
  }
  return $full
}

function Assert-SynonProductIdentity {
  param(
    [Parameter(Mandatory = $true)][string]$TrustedIdentityPath,
    [Parameter(Mandatory = $true)][string]$CandidateRoot
  )

  $trustedPath = Resolve-SynonTrustedIdentityPath -Path $TrustedIdentityPath
  $candidatePath = Join-Path ([System.IO.Path]::GetFullPath($CandidateRoot)) 'product-identity.json'
  $trusted = Read-SynonProductIdentity -Path $trustedPath
  [void](Read-SynonProductIdentity -Path $candidatePath)
  $trustedBytes = [Convert]::ToBase64String([System.IO.File]::ReadAllBytes($trustedPath))
  $candidateBytes = [Convert]::ToBase64String([System.IO.File]::ReadAllBytes($candidatePath))
  if ($trustedBytes -cne $candidateBytes) {
    throw 'Release product identity does not match the trusted authority'
  }
  return $trusted
}

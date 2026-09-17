function Test-SynonReleaseEnvironmentKey {
  param([Parameter(Mandatory = $true)][string]$Name)

  return $Name -match '^(SYNON_|FEISHU_|WECHAT_|OPERON_)' -or $Name -in @('WSL_DISTRO_NAME', 'WSL_INTEROP')
}

function Invoke-SynonReleaseTestEnvironment {
  param(
    [Parameter(Mandatory = $true)][string]$Root,
    [Parameter(Mandatory = $true)][scriptblock]$Action
  )

  $pathRoot = [System.IO.Path]::GetPathRoot($Root)
  $windowsRootRelative = [System.IO.Path]::DirectorySeparatorChar -eq '\' -and $pathRoot -in @('\', '/')
  if (-not [System.IO.Path]::IsPathRooted($Root) -or $pathRoot.EndsWith(':') -or $windowsRootRelative) {
    throw 'Release test environment root must be an absolute path'
  }
  $rootFull = [System.IO.Path]::GetFullPath($Root)
  $saved = @{}
  foreach ($entry in [Environment]::GetEnvironmentVariables('Process').GetEnumerator()) {
    $saved[$entry.Key] = $entry.Value
  }
  try {
    foreach ($key in $saved.Keys) {
      if (Test-SynonReleaseEnvironmentKey -Name $key) { Remove-Item -LiteralPath "Env:$key" -ErrorAction Stop }
    }
    # A persisted data-directory pointer overrides SYNON_HOME. Both authorities
    # must be private before any installed CLI (including model-smoke) can run.
    $env:SYNON_HOME = Join-Path $rootFull 'release-runtime-home'
    $env:SYNON_DATA_DIR_CONTROL = Join-Path $rootFull 'data-dir.json'
    & $Action
  } finally {
    # Include variables introduced by the action and restore system variables
    # even if a nested probe throws before restoring its own temporary PATH.
    foreach ($entry in [Environment]::GetEnvironmentVariables('Process').GetEnumerator()) {
      if (-not $saved.ContainsKey($entry.Key)) {
        Remove-Item -LiteralPath "Env:$($entry.Key)" -ErrorAction Stop
      }
    }
    foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
  }
}

function Wait-SynonReleaseHTTPReady {
  param(
    [Parameter(Mandatory = $true)][uri]$BaseUri,
    [Parameter(Mandatory = $true)][scriptblock]$HasExited,
    [ValidateRange(1, 60000)][int]$TimeoutMilliseconds = 15000,
    [ValidateRange(1, 10000)][int]$RequestTimeoutMilliseconds = 2000
  )

  if ($BaseUri.Scheme -ne 'http' -or $BaseUri.Host -ne '127.0.0.1' -or $BaseUri.UserInfo -ne '') {
    throw 'Release readiness probe requires an unauthenticated loopback HTTP endpoint'
  }
  $handler = [System.Net.Http.HttpClientHandler]::new()
  $handler.UseProxy = $false
  $handler.AllowAutoRedirect = $false
  $client = [System.Net.Http.HttpClient]::new($handler)
  $client.Timeout = [TimeSpan]::FromMilliseconds($RequestTimeoutMilliseconds)
  $deadline = [System.Threading.CancellationTokenSource]::new($TimeoutMilliseconds)
  try {
    while (-not $deadline.IsCancellationRequested) {
      if (& $HasExited) { return $false }
      try {
        $payloads = @{}
        foreach ($path in @('/api/health', '/')) {
          $response = $null
          try {
            $response = $client.GetAsync([uri]::new($BaseUri, $path), $deadline.Token).GetAwaiter().GetResult()
            [void]$response.EnsureSuccessStatusCode()
            $payloads[$path] = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
          } finally {
            if ($null -ne $response) { $response.Dispose() }
          }
        }
        if ($payloads['/api/health'] -match '"status"\s*:\s*"healthy"' -and $payloads['/'] -match '<!doctype html>') {
          return $true
        }
      } catch {
        # Connection refusal, an incomplete response or request timeout may be
        # startup transients, but never extend the shared readiness deadline.
        if ($deadline.IsCancellationRequested) { return $false }
      }
      [void]$deadline.Token.WaitHandle.WaitOne(100)
    }
    return $false
  } finally {
    $deadline.Dispose()
    $client.Dispose()
    $handler.Dispose()
  }
}

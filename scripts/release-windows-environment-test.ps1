[CmdletBinding()]
param(
  [string]$LifecycleScript = (Join-Path $PSScriptRoot 'release-windows-test.ps1')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http
$suiteRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('synon-release-environment-test-' + [Guid]::NewGuid().ToString('N'))
$fixtureRoot = Join-Path $suiteRoot 'fixture'
$outside = Join-Path $suiteRoot 'outside-runtime'
$ownedTemp = Join-Path $suiteRoot 'owned-temp'
$saved = @{}
foreach ($key in @('TEMP', 'TMP', 'SYNON_CONFIG', 'SYNON_DATA_DIR_CONTROL', 'SYNON_RUNNER_PROVIDER', 'SYNON_FUTURE_TEST_VALUE', 'FEISHU_APP_SECRET', 'WECHAT_BOT_TOKEN', 'OPERON_SERVICE_URL', 'WSL_DISTRO_NAME', 'WSL_INTEROP')) {
  $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
$passed = $false
try {
  New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'scripts'), $outside, $ownedTemp | Out-Null
  $config = Join-Path $outside 'private-config.json'
  $pointer = Join-Path $outside 'data-dir.json'
  $marker = Join-Path $outside 'marker'
  Set-Content -LiteralPath $config -Value 'SYNTHETIC PRIVATE CONFIG: MUST NOT BE READ' -NoNewline
  @{ schemaVersion = 1; current = $outside } | ConvertTo-Json | Set-Content -LiteralPath $pointer
  Set-Content -LiteralPath $marker -Value 'preserve-me' -NoNewline
  $hashes = @{}
  foreach ($path in @($config, $pointer, $marker)) { $hashes[$path] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash }
  $env:TEMP = $ownedTemp
  $env:TMP = $ownedTemp
  $env:SYNON_CONFIG = $config
  $env:SYNON_DATA_DIR_CONTROL = $pointer
  $env:SYNON_RUNNER_PROVIDER = 'openai_chat'
  $env:SYNON_FUTURE_TEST_VALUE = ''
  $env:FEISHU_APP_SECRET = 'fixture-feishu-secret'
  $env:WECHAT_BOT_TOKEN = 'fixture-wechat-token'
  $env:OPERON_SERVICE_URL = 'https://example.invalid/must-not-contact'
  $env:WSL_DISTRO_NAME = 'synthetic-must-not-restart'
  $env:WSL_INTEROP = 'synthetic-must-not-use'

  # Exercise the actual lifecycle entry without executing a package or reading
  # any inherited configuration. The first installer call only probes its env.
  @'
param([string]$Archive, [string]$InstallDir, [switch]$NoRunCheck, [switch]$EnvironmentProbe)
if (-not $EnvironmentProbe) {
  $result = & (Get-Command pwsh).Source -NoProfile -NonInteractive -File $PSCommandPath -EnvironmentProbe
  if ($LASTEXITCODE -ne 0) { throw ($result -join [Environment]::NewLine) }
  if ($result -ne 'ENVIRONMENT_PROBE_OK') { throw 'CHILD_PROBE_DID_NOT_EXECUTE' }
  throw 'ENVIRONMENT_PROBE_STOPPED_BEFORE_INSTALL'
}
$unexpected = @(Get-ChildItem Env: | Where-Object {
  ($_.Name -match '^(SYNON_|FEISHU_|WECHAT_|OPERON_)' -or $_.Name -in @('WSL_DISTRO_NAME', 'WSL_INTEROP')) -and
    $_.Name -notin @('SYNON_HOME', 'SYNON_DATA_DIR_CONTROL')
})
if ($unexpected.Count -ne 0) {
  Write-Output 'PRODUCT_ENVIRONMENT_INHERITED'; exit 13
}
if ([string]::IsNullOrWhiteSpace($env:SYNON_HOME) -or [string]::IsNullOrWhiteSpace($env:SYNON_DATA_DIR_CONTROL)) {
  throw 'PRIVATE_RUNTIME_ENVIRONMENT_MISSING'
}
$runtimeRoot = Split-Path -Parent $env:SYNON_HOME
$pointerParent = Split-Path -Parent $env:SYNON_DATA_DIR_CONTROL
if (-not [System.IO.Path]::IsPathRooted($env:SYNON_DATA_DIR_CONTROL) -or $pointerParent -ne $runtimeRoot) {
  throw 'PRIVATE_RUNTIME_POINTER_ESCAPED'
}
if (Test-Path -LiteralPath $env:SYNON_DATA_DIR_CONTROL) { throw 'PRIVATE_RUNTIME_POINTER_ALREADY_EXISTS' }
Write-Output 'ENVIRONMENT_PROBE_OK'
'@ | Set-Content -LiteralPath (Join-Path $fixtureRoot 'scripts/install-release.ps1')

  $observed = ''
  $locks = @()
  try {
    # Exclusive handles make even accidental reads of these synthetic host
    # files fail. No real user configuration is accessed by this regression.
    foreach ($path in @($config, $pointer, $marker)) {
      $locks += [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
    }
    try { & $LifecycleScript -Archive (Join-Path $suiteRoot 'unused.tar.gz') -RepoRoot $fixtureRoot }
    catch { $observed = $_.Exception.Message }
  } finally { foreach ($handle in $locks) { $handle.Dispose() } }
  foreach ($path in $hashes.Keys) {
    if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $hashes[$path]) { throw 'External sentinel changed' }
  }
  if ($observed -ne 'ENVIRONMENT_PROBE_STOPPED_BEFORE_INSTALL') {
    throw "Lifecycle environment probe failed: $observed"
  }
  if ($env:SYNON_CONFIG -ne $config -or $env:SYNON_DATA_DIR_CONTROL -ne $pointer -or $env:SYNON_RUNNER_PROVIDER -ne 'openai_chat' -or
      $env:FEISHU_APP_SECRET -ne 'fixture-feishu-secret' -or $env:WECHAT_BOT_TOKEN -ne 'fixture-wechat-token' -or
      $env:OPERON_SERVICE_URL -ne 'https://example.invalid/must-not-contact') {
    throw 'Lifecycle did not restore its caller environment after failure'
  }
  Write-Output 'PASS actual lifecycle child environment, locked external sentinels, and caller restoration on failure'

  . (Join-Path $PSScriptRoot 'release-test-environment.ps1')
  function Get-TestEnvironment {
    $values = @{}
    foreach ($entry in [Environment]::GetEnvironmentVariables('Process').GetEnumerator()) { $values[$entry.Key] = $entry.Value }
    return $values
  }
  function Assert-EnvironmentRestored {
    param([System.Collections.IDictionary]$Expected)
    $actual = Get-TestEnvironment
    if ($actual.Count -ne $Expected.Count) {
      $added = @($actual.Keys | Where-Object { -not $Expected.Contains($_) }) -join ','
      $missing = @($Expected.Keys | Where-Object { -not $actual.Contains($_) }) -join ','
      throw "Process environment key count changed; added=$added missing=$missing"
    }
    foreach ($key in $Expected.Keys) {
      if (-not $actual.Contains($key) -or $actual[$key] -cne $Expected[$key]) { throw "Process environment was not restored: $key" }
    }
  }
  $before = Get-TestEnvironment
  Invoke-SynonReleaseTestEnvironment -Root $ownedTemp -Action {
    foreach ($key in @('SYNON_CONFIG', 'SYNON_RUNNER_PROVIDER', 'FEISHU_APP_SECRET', 'WECHAT_BOT_TOKEN', 'OPERON_SERVICE_URL')) {
      if ([Environment]::GetEnvironmentVariable($key, 'Process')) { throw "Inherited product variable: $key" }
    }
    foreach ($key in @('SystemRoot', 'ComSpec', 'PATH', 'TEMP', 'TMP', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA')) {
      if ([Environment]::GetEnvironmentVariable($key, 'Process') -cne $before[$key]) { throw "Required Windows environment changed: $key" }
    }
    $env:SYNON_CONFIG = 'synthetic-child-change'
    $env:SYNON_NEW_TEST_VARIABLE = 'synthetic-added-value'
    $env:RELEASE_TEST_ADDED_VALUE = 'synthetic-added-value'
    $env:PATH = ''
  }
  Assert-EnvironmentRestored -Expected $before
  Write-Output 'PASS complete environment restoration on normal return; system environment preserved during isolation'

  $thrown = $false
  try {
    Invoke-SynonReleaseTestEnvironment -Root $ownedTemp -Action {
      $env:SYNON_HOME = 'synthetic-child-change'
      $env:RELEASE_TEST_ADDED_VALUE = 'synthetic-added-value'
      $env:PATH = ''
      throw 'SYNTHETIC_ACTION_FAILURE'
    }
  } catch {
    if ($_.Exception.Message -ne 'SYNTHETIC_ACTION_FAILURE') { throw }
    $thrown = $true
  }
  if (-not $thrown) { throw 'Environment wrapper swallowed action failure' }
  Assert-EnvironmentRestored -Expected $before
  Write-Output 'PASS complete environment restoration on exception without swallowing failure'

  Invoke-SynonReleaseTestEnvironment -Root $ownedTemp -Action {
    $outer = Get-TestEnvironment
    Invoke-SynonReleaseTestEnvironment -Root (Join-Path $ownedTemp 'nested') -Action {
      if ((Split-Path -Parent $env:SYNON_DATA_DIR_CONTROL) -ne (Join-Path $ownedTemp 'nested')) { throw 'Nested pointer not private' }
    }
    Assert-EnvironmentRestored -Expected $outer
  }
  Assert-EnvironmentRestored -Expected $before
  Write-Output 'PASS nested environment scopes restore their own caller'

  $relativeRejected = $false
  try { Invoke-SynonReleaseTestEnvironment -Root 'relative-root' -Action { throw 'MUST_NOT_ENTER' } }
  catch {
    if ($_.Exception.Message -ne 'Release test environment root must be an absolute path') { throw }
    $relativeRejected = $true
  }
  if (-not $relativeRejected) { throw 'Relative environment root was accepted' }
  Assert-EnvironmentRestored -Expected $before
  Write-Output 'PASS relative environment roots rejected before mutation'

  $driveRelativeRejected = $false
  try { Invoke-SynonReleaseTestEnvironment -Root 'C:relative-root' -Action { throw 'MUST_NOT_ENTER' } }
  catch {
    if ($_.Exception.Message -ne 'Release test environment root must be an absolute path') { throw }
    $driveRelativeRejected = $true
  }
  if (-not $driveRelativeRejected) { throw 'Drive-relative environment root was accepted' }
  Assert-EnvironmentRestored -Expected $before
  Write-Output 'PASS drive-relative environment roots rejected before mutation'

  $nonLoopbackRejected = $false
  try { Wait-SynonReleaseHTTPReady -BaseUri 'https://example.invalid/' -HasExited { throw 'MUST_NOT_ENTER' } }
  catch {
    if ($_.Exception.Message -ne 'Release readiness probe requires an unauthenticated loopback HTTP endpoint') { throw }
    $nonLoopbackRejected = $true
  }
  if (-not $nonLoopbackRejected) { throw 'External readiness endpoint was accepted' }
  Write-Output 'PASS non-loopback readiness endpoint rejected before traffic'

  # A real loopback HTTP peer exercises connection/body stalls and recovery.
  # It is only a test server, never a product binary or external endpoint.
  Add-Type -TypeDefinition @'
using System;
using System.Collections.Concurrent;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
public sealed class SynonReleaseHTTPFixture : IDisposable {
  readonly TcpListener listener = new TcpListener(IPAddress.Loopback, 0);
  readonly CancellationTokenSource stopped = new CancellationTokenSource();
  readonly ConcurrentBag<TcpClient> clients = new ConcurrentBag<TcpClient>();
  readonly string mode;
  readonly string redirectBase;
  readonly Task accept;
  int requests;
  public int Port { get; private set; }
  public int Requests { get { return Volatile.Read(ref requests); } }
  public SynonReleaseHTTPFixture(string mode) : this(mode, null) { }
  public SynonReleaseHTTPFixture(string mode, string redirectBase) {
    this.mode = mode; this.redirectBase = redirectBase;
    listener.Start(); Port = ((IPEndPoint)listener.LocalEndpoint).Port;
    accept = Task.Run(async () => {
      while (!stopped.IsCancellationRequested) {
        TcpClient client;
        try { client = await listener.AcceptTcpClientAsync(); }
        catch (ObjectDisposedException) { break; }
        catch (SocketException) { if (stopped.IsCancellationRequested) break; throw; }
        clients.Add(client);
        _ = Task.Run(() => Serve(client));
      }
    });
  }
  async Task Serve(TcpClient client) {
    try {
      using (client) using (var stream = client.GetStream()) {
        var bytes = new byte[4096];
        int length = await stream.ReadAsync(bytes, 0, bytes.Length, stopped.Token);
        string request = Encoding.ASCII.GetString(bytes, 0, length);
        int count = Interlocked.Increment(ref requests);
        if (mode == "stall-headers") { await Task.Delay(30000, stopped.Token); return; }
        bool health = request.StartsWith("GET /api/health ");
        bool unavailable = mode == "recover" && count == 1;
        string body = health ? "{\"status\":\"healthy\"}" : "<!doctype html><title>fixture</title>";
        if (mode == "bad-html" && !health) body = "not the packaged client";
        if (mode == "stall-body") body = "{";
        string status = mode == "redirect" ? "302 Found" : (unavailable ? "503 Unavailable" : "200 OK");
        string location = mode == "redirect" ? "\r\nLocation: " + redirectBase + (health ? "/api/health" : "/") : "";
        string header = "HTTP/1.1 " + status + location +
          "\r\nContent-Length: " + (mode == "stall-body" ? 10000 : Encoding.UTF8.GetByteCount(body)) +
          "\r\nConnection: close\r\n\r\n";
        bytes = Encoding.UTF8.GetBytes(header + body);
        await stream.WriteAsync(bytes, 0, bytes.Length, stopped.Token);
        await stream.FlushAsync(stopped.Token);
        if (mode == "stall-body") await Task.Delay(30000, stopped.Token);
      }
    } catch (OperationCanceledException) { }
      catch (IOException) { }
      catch (ObjectDisposedException) { }
  }
  public void Dispose() {
    stopped.Cancel(); listener.Stop();
    foreach (var client in clients) client.Dispose();
    if (!accept.Wait(2000)) throw new Exception("HTTP fixture accept loop did not terminate");
    stopped.Dispose();
  }
}
'@
  foreach ($mode in @('healthy', 'recover', 'bad-html', 'stall-headers', 'stall-body')) {
    $peer = [SynonReleaseHTTPFixture]::new($mode)
    try {
      $watch = [Diagnostics.Stopwatch]::StartNew()
      $ready = Wait-SynonReleaseHTTPReady -BaseUri "http://127.0.0.1:$($peer.Port)" -HasExited { $false } -TimeoutMilliseconds 700 -RequestTimeoutMilliseconds 150
      $watch.Stop()
      $expected = $mode -in @('healthy', 'recover')
      if ($ready -ne $expected) { throw "Unexpected readiness for HTTP fixture: $mode" }
      if ($peer.Requests -lt 1) { throw 'Readiness fixture was not actually contacted' }
      if ($watch.ElapsedMilliseconds -gt 2500) { throw "Readiness deadline was not bounded for $mode" }
      Write-Output "PASS actual HTTP $mode; ready=$ready elapsed_ms=$($watch.ElapsedMilliseconds)"
    } finally { $peer.Dispose() }
  }
  $peer = [SynonReleaseHTTPFixture]::new('healthy')
  try {
    if (Wait-SynonReleaseHTTPReady -BaseUri "http://127.0.0.1:$($peer.Port)" -HasExited { $true }) { throw 'Exited process was reported ready' }
    if ($peer.Requests -ne 0) { throw 'Exited process probe contacted an unrelated port owner' }
  } finally { $peer.Dispose() }
  Write-Output 'PASS exited child is rejected without HTTP traffic'

  $redirectTarget = [SynonReleaseHTTPFixture]::new('healthy')
  $redirector = $null
  try {
    $redirector = [SynonReleaseHTTPFixture]::new('redirect', "http://127.0.0.1:$($redirectTarget.Port)")
    $ready = Wait-SynonReleaseHTTPReady -BaseUri "http://127.0.0.1:$($redirector.Port)" -HasExited { $false } -TimeoutMilliseconds 700 -RequestTimeoutMilliseconds 150
    if ($redirector.Requests -lt 1) { throw 'Redirect fixture was not actually contacted' }
    if ($redirectTarget.Requests -ne 0) { throw 'Readiness probe followed a redirect to a different target' }
    if ($ready) { throw 'Redirected target was incorrectly accepted as ready' }
  } finally {
    if ($null -ne $redirector) { $redirector.Dispose() }
    $redirectTarget.Dispose()
  }
  Write-Output 'PASS actual HTTP 302 is not followed and cannot supply readiness from another target'

  foreach ($rootRelative in @('\root-relative', '/root-relative')) {
    $rootRelativeRejected = $false
    try { Invoke-SynonReleaseTestEnvironment -Root $rootRelative -Action { throw 'MUST_NOT_ENTER' } }
    catch {
      if ($_.Exception.Message -ne 'Release test environment root must be an absolute path') { throw }
      $rootRelativeRejected = $true
    }
    if (-not $rootRelativeRejected) { throw 'Windows root-relative environment root was accepted' }
    Assert-EnvironmentRestored -Expected $before
    Write-Output "PASS Windows root-relative path rejected before mutation: $rootRelative"
  }
  Write-Output 'WINDOWS_RELEASE_ENVIRONMENT_TEST=yes tests=16'
  $passed = $true
} finally {
  foreach ($key in $saved.Keys) {
    if ($null -eq $saved[$key]) { Remove-Item -LiteralPath "Env:$key" -ErrorAction SilentlyContinue }
    else { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
  }
  if ($passed) {
    if ((Split-Path -Leaf $suiteRoot) -notlike 'synon-release-environment-test-*') { throw 'Unsafe test cleanup root' }
    Remove-Item -LiteralPath $suiteRoot -Recurse -Force
  } else {
    Write-Warning "Release environment regression evidence preserved at $suiteRoot"
  }
}

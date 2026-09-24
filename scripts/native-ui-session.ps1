#requires -Version 7.4
<#
.SYNOPSIS
Starts, inspects, or stops a disposable native noXa test environment.
.DESCRIPTION
Run in PowerShell 7.4+ on Windows with Docker Desktop, Go, and Node/npm.
Starts actual Wails clients and provisions disposable roles-v1 accounts.
The test action runs the client backend's live integration tests.
SkipBuild skips server/client builds; provisioning tools are always built.
All ports bind loopback. DEV_MODE permits locally generated TLS certificates.
Run IDs are single-use. Stop terminates only recorded native processes and stops
only recorded, correctly labeled containers. Evidence and data are retained.
.EXAMPLE
./scripts/native-ui-session.ps1 start -RunId native-demo -BasePort 13583
.EXAMPLE
./scripts/native-ui-session.ps1 status -RunId native-demo
.EXAMPLE
./scripts/native-ui-session.ps1 stop -RunId native-demo
.EXAMPLE
./scripts/native-ui-session.ps1 start -RunId setup-check -BasePort 14583 -SkipBuild -ServerBinary C:/fixtures/server.exe -ClientBinary C:/fixtures/client.exe -NoClients
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory, Position = 0)][ValidateSet('start', 'status', 'stop', 'test')][string]$Action,
    [Parameter(Mandatory)][ValidatePattern('^[a-z0-9][a-z0-9-]{0,39}$')][string]$RunId,
    [ValidateRange(1024, 65526)][int]$BasePort = 13583,
    [switch]$SkipBuild,
    [string]$ServerBinary,
    [string]$ClientBinary,
    [switch]$NoClients
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows) { throw 'This runner requires Windows for the native Wails client.' }
$repoRoot = Split-Path -Parent $PSScriptRoot
$runRoot = [IO.Path]::GetFullPath((Join-Path $repoRoot "temp/native-ui-$RunId"))
$manifestPath = Join-Path $runRoot 'session.json'
$labelKey = 'noxa.native-ui.run'
$session = $null

function Invoke-Checked([string]$Program, [string[]]$Arguments) {
    $output = & $Program @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "$Program failed ($LASTEXITCODE): $($output -join [Environment]::NewLine)" }
    return $output
}

function Save-Session {
    $session | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $manifestPath -Encoding utf8
}

function Assert-RunPath([string]$Path) {
    $full = [IO.Path]::GetFullPath($Path)
    if (-not $full.StartsWith($runRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing path outside the run directory: $full"
    }
    return $full
}

function Get-RecordedProcess($Record) {
    $expectedPath = Assert-RunPath $Record.executable
    $process = Get-Process -Id $Record.pid -ErrorAction SilentlyContinue
    if ($null -eq $process) { return $null }
    if (-not [string]::Equals($process.Path, $expectedPath, [StringComparison]::OrdinalIgnoreCase) -or
        $process.StartTime.ToUniversalTime().Ticks -ne ([datetime]$Record.startedUTC).ToUniversalTime().Ticks) {
        throw "PID $($Record.pid) no longer identifies the saved $($Record.name) executable; refusing to act."
    }
    return $process
}

function Get-RecordedContainer($Record) {
    $container = (Invoke-Checked docker @('inspect', $Record.id) | Out-String | ConvertFrom-Json)[0]
    $label = $container.Config.Labels.PSObject.Properties[$labelKey]
    if ($null -eq $label -or $label.Value -ne $RunId -or
        $container.Id -ne $Record.id -or $container.Name -ne ('/' + $Record.name)) {
        throw "Container ownership mismatch for $($Record.name); refusing to act."
    }
    return $container
}

function Stop-Session {
    # This is bounded process termination, not a claim of graceful UI shutdown.
    $errors = [Collections.Generic.List[string]]::new()
    foreach ($record in @($session.processes | Sort-Object { $_.name -eq 'server' })) {
        try {
            $process = Get-RecordedProcess $record
            if ($null -ne $process) {
                Stop-Process -InputObject $process -Force
                $process.WaitForExit(10000) | Out-Null
                if (-not $process.HasExited) { throw "Process $($record.pid) did not stop." }
            }
        } catch { $errors.Add($_.Exception.Message) }
    }
    foreach ($record in $session.containers) {
        try {
            $container = Get-RecordedContainer $record
            if ($container.State.Running) { Invoke-Checked docker @('stop', '-t', '10', $record.id) | Out-Null }
        } catch { $errors.Add($_.Exception.Message) }
    }
    $session.state = if ($errors.Count) { 'stop-failed' } else { 'stopped' }
    Save-Session
    if ($errors.Count) { throw ($errors -join [Environment]::NewLine) }
}

function Get-CleanEnvironment {
    $overrides = @{}
    foreach ($entry in Get-ChildItem Env:) {
        if ($entry.Name -like 'NOXA_*' -or $entry.Name -like 'WEBVIEW2_*') { $overrides[$entry.Name] = $null }
    }
    return $overrides
}

function Start-RecordedProcess([string]$Name, [string]$Executable, [string]$Directory, [hashtable]$Environment, [switch]$Visible) {
    $logRoot = Join-Path $runRoot 'evidence'
    $style = if ($Visible) { 'Normal' } else { 'Hidden' }
    $process = Start-Process -FilePath $Executable -WorkingDirectory $Directory -Environment $Environment -WindowStyle $style -PassThru `
        -RedirectStandardOutput (Join-Path $logRoot "$Name.stdout.log") -RedirectStandardError (Join-Path $logRoot "$Name.stderr.log")
    $record = [pscustomobject]@{
        name = $Name; pid = $process.Id; executable = $Executable
        startedUTC = $process.StartTime.ToUniversalTime().ToString('o')
        sha256 = (Get-FileHash -LiteralPath $Executable -Algorithm SHA256).Hash
        directory = $Directory
    }
    $session.processes += $record
    Save-Session
    return $record
}

function Wait-Ready([scriptblock]$Condition, [string]$Description, [int]$Seconds = 60) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Timed out waiting for $Description. Inspect $runRoot/evidence."
}

if ($Action -ne 'start') {
    $session = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($session.runId -ne $RunId -or $session.root -ne $runRoot) { throw 'Session manifest does not match requested run.' }
    if ($Action -eq 'stop') { Stop-Session; Write-Output "Stopped $RunId; retained $runRoot"; return }
    if ($Action -eq 'test') {
        $server = @($session.processes | Where-Object name -eq 'server')
        if ($server.Count -ne 1 -or $null -eq (Get-RecordedProcess $server[0])) { throw 'The disposable server is not running.' }
        $testEnv = Get-Content -LiteralPath (Join-Path $runRoot 'secrets/live-environment.json') -Raw | ConvertFrom-Json -AsHashtable
        $previous = @{}
        Push-Location (Join-Path $repoRoot 'client')
        try {
            foreach ($key in $testEnv.Keys) {
                if ($key -notlike 'NOXA_LIVE_*') { throw 'Unexpected live test environment key.' }
                $previous[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
                [Environment]::SetEnvironmentVariable($key, $testEnv[$key], 'Process')
            }
            & go test -run '^TestLive' -count=1 -v . *> (Join-Path $runRoot 'evidence/live-tests.log')
            $testExit = $LASTEXITCODE
            Get-Content -LiteralPath (Join-Path $runRoot 'evidence/live-tests.log')
            if ($testExit -ne 0) { throw 'Live integration tests failed; inspect evidence/live-tests.log.' }
        } finally {
            foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
            Pop-Location
        }
        return
    }
    $processStatus = foreach ($record in $session.processes) {
        [pscustomobject]@{ name = $record.name; pid = $record.pid; running = ($null -ne (Get-RecordedProcess $record)); executable = $record.executable }
    }
    $containerStatus = foreach ($record in $session.containers) {
        [pscustomobject]@{ name = $record.name; running = (Get-RecordedContainer $record).State.Running }
    }
    [pscustomobject]@{ runId = $RunId; state = $session.state; address = $session.address; evidence = "$runRoot/evidence"; processes = @($processStatus); containers = @($containerStatus) } | ConvertTo-Json -Depth 5
    return
}

if (Test-Path -LiteralPath $runRoot) { throw "Run directory already exists; choose a new RunId. Existing data retained: $runRoot" }
if ($SkipBuild) {
    if (-not $ServerBinary -or -not $ClientBinary) { throw '-SkipBuild requires explicit -ServerBinary and -ClientBinary paths.' }
    $ServerBinary = (Resolve-Path -LiteralPath $ServerBinary).Path
    $ClientBinary = (Resolve-Path -LiteralPath $ClientBinary).Path
}
Invoke-Checked docker @('info', '--format', '{{.ServerVersion}}') | Out-Null
# Check every reserved TCP port plus the application's UDP port before creating resources.
foreach ($offset in @(0, 2, 3, 4, 5, 6, 7, 8)) {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $BasePort + $offset)
    try { $listener.Start() } finally { $listener.Stop() }
}
$udp = [Net.Sockets.UdpClient]::new()
try { $udp.Client.Bind([Net.IPEndPoint]::new([Net.IPAddress]::Loopback, $BasePort + 1)) } finally { $udp.Dispose() }
foreach ($directory in @('bin', 'tools', 'server', 'profiles', 'evidence', 'secrets')) {
    New-Item -ItemType Directory -Path (Join-Path $runRoot $directory) -Force | Out-Null
}
# Keep disposable credentials readable only by this Windows account and SYSTEM.
$secretAcl = [Security.AccessControl.DirectorySecurity]::new()
$secretAcl.SetAccessRuleProtection($true, $false)
foreach ($sid in @([Security.Principal.WindowsIdentity]::GetCurrent().User, [Security.Principal.SecurityIdentifier]::new('S-1-5-18'))) {
    $secretAcl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
}
Set-Acl -LiteralPath (Join-Path $runRoot 'secrets') -AclObject $secretAcl
# Keep generated Go tooling outside root-module package discovery.
Set-Content -LiteralPath (Join-Path $runRoot 'go.mod') -Value 'module native-ui-artifacts'
$session = [pscustomobject]@{
    runId = $RunId; root = $runRoot; state = 'starting'; createdUTC = [DateTime]::UtcNow.ToString('o')
    address = "127.0.0.1:$BasePort"; basePort = $BasePort; processes = @(); containers = @(); profiles = @()
    sourceCommit = ''; buildFlags = ''; skipBuild = [bool]$SkipBuild
}
Save-Session
$savedLocation = Get-Location
try {
    Set-Location -LiteralPath $repoRoot
    $session.sourceCommit = (Invoke-Checked git @('rev-parse', 'HEAD') | Out-String).Trim()
    if (-not $SkipBuild) {
        $session.buildFlags = (Invoke-Checked go @('run', './cmd/version', '-format', 'ldflags') | Out-String).Trim()
        Save-Session
        $ServerBinary = Join-Path $runRoot 'bin/server.exe'
        Invoke-Checked go @('build', '-trimpath', "-ldflags=$($session.buildFlags)", '-o', $ServerBinary, './cmd/server') |
            Set-Content -LiteralPath (Join-Path $runRoot 'evidence/server-build.log')
        Set-Location -LiteralPath (Join-Path $repoRoot 'client')
        $wailsVersion = (Invoke-Checked go @('list', '-m', '-f', '{{.Version}}', 'github.com/wailsapp/wails/v2') | Out-String).Trim()
        if ($wailsVersion -notmatch '^v\d+\.\d+\.\d+$') { throw 'Expected a pinned stable Wails version in client/go.mod.' }
        $previousGoBin = $env:GOBIN
        try {
            $env:GOBIN = Join-Path $runRoot 'tools'
            Invoke-Checked go @('install', "github.com/wailsapp/wails/v2/cmd/wails@$wailsVersion") |
                Set-Content -LiteralPath (Join-Path $runRoot 'evidence/wails-install.log')
        } finally { $env:GOBIN = $previousGoBin }
        Set-Location -LiteralPath (Join-Path $repoRoot 'client/frontend')
        Invoke-Checked npm.cmd @('ci') | Set-Content -LiteralPath (Join-Path $runRoot 'evidence/npm-ci.log')
        Invoke-Checked npm.cmd @('run', 'build') | Set-Content -LiteralPath (Join-Path $runRoot 'evidence/frontend-build.log')
        Set-Location -LiteralPath (Join-Path $repoRoot 'client')
        Invoke-Checked (Join-Path $runRoot 'tools/wails.exe') @('build', '-s', '-m', '-nosyncgomod', '-skipbindings', '-trimpath', '-o', 'native-ui-client.exe', '-ldflags', $session.buildFlags) |
            Set-Content -LiteralPath (Join-Path $runRoot 'evidence/client-build.log')
        $ClientBinary = Join-Path $repoRoot 'client/build/bin/native-ui-client.exe'
    } else { Copy-Item -LiteralPath $ServerBinary -Destination (Join-Path $runRoot 'bin/server.exe') }
    Copy-Item -LiteralPath $ClientBinary -Destination (Join-Path $runRoot 'bin/client.exe')
    $password = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
    $envFile = Join-Path $runRoot 'secrets/postgres.env'
    @('POSTGRES_USER=noxa_ui', 'POSTGRES_DB=noxa_ui', "POSTGRES_PASSWORD=$password") | Set-Content -LiteralPath $envFile -Encoding ascii
    $images = @{
        postgres = 'postgres:16-alpine@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777'
        redis = 'redis:7-alpine@sha256:e7723ff73d963f5cc6d9c4643ea3d989527a402a319239054e9472a7fb9219a2'
    }
    foreach ($kind in @('postgres', 'redis')) {
        $containerName = "noxa-native-$RunId-$kind"
        $hostPort = $BasePort + $(if ($kind -eq 'postgres') { 7 } else { 8 })
        $internalPort = if ($kind -eq 'postgres') { 5432 } else { 6379 }
        $dockerArgs = @('run', '-d', '--name', $containerName, '--label', "$labelKey=$RunId", '-p', "127.0.0.1:${hostPort}:$internalPort")
        if ($kind -eq 'postgres') { $dockerArgs += @('--env-file', $envFile) }
        $dockerArgs += $images[$kind]
        $containerId = (Invoke-Checked docker $dockerArgs | Select-Object -Last 1).ToString().Trim()
        $session.containers += [pscustomobject]@{ name = $containerName; id = $containerId; kind = $kind; image = $images[$kind] }
        Save-Session
        Wait-Ready {
            if ($kind -eq 'postgres') { & docker exec $containerId pg_isready -U noxa_ui -d noxa_ui *> $null }
            else { & docker exec $containerId redis-cli ping *> $null }
            return $LASTEXITCODE -eq 0
        } "$kind readiness"
    }
    $serverEnv = Get-CleanEnvironment
    $serverEnv.NOXA_DATABASE_URL = "postgres://noxa_ui:$password@127.0.0.1:$($BasePort + 7)/noxa_ui?sslmode=disable"
    $serverEnv.NOXA_REDIS_ADDR = "127.0.0.1:$($BasePort + 8)"
    $serverEnv.NOXA_SERVER_NAME = "NATIVE-UI-$RunId"
    $serverEnv.NOXA_DEV_MODE = 'true'
    $serverEnv.NOXA_TLS_ENABLED = 'true'
    $serverEnv.NOXA_FILE_TLS_ENABLED = 'true'
    $portNames = @('TCP_ADDR', 'UDP_ADDR', 'GRPC_ADDR', 'HEALTH_ADDR', 'QUERY_ADDR', 'FILE_ADDR', 'QUERY_SSH_ADDR')
    for ($offset = 0; $offset -lt $portNames.Count; $offset++) { $serverEnv['NOXA_' + $portNames[$offset]] = "127.0.0.1:$($BasePort + $offset)" }
    # The database lease requires provisioning while the server is stopped.
    Set-Location -LiteralPath $repoRoot
    foreach ($tool in @('adduser', 'role-setup')) {
        Invoke-Checked go @('build', '-o', (Join-Path $runRoot "tools/$tool.exe"), "./cmd/$tool") | Out-Null
    }
    $previousDatabase = $env:NOXA_DATABASE_URL
    $previousChatKey = $env:NOXA_CHAT_MASTER_KEY
    $credentials = @{}
    try {
        $env:NOXA_DATABASE_URL = $serverEnv.NOXA_DATABASE_URL
        $env:NOXA_CHAT_MASTER_KEY = $null
        foreach ($account in @('ALICE', 'BOB', 'ADMIN')) {
            $accountPassword = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
            $provisionOutput = (& (Join-Path $runRoot 'tools/adduser.exe') -nickname "native-$($account.ToLowerInvariant())" -password $accountPassword 2>&1 | Out-String)
            if ($LASTEXITCODE -ne 0 -or $provisionOutput -notmatch 'unique_id: (\S+)') { throw "Could not provision $account; credential-bearing output suppressed." }
            $credentials[$account] = @{ uid = $Matches[1]; password = $accountPassword }
        }
        $credentials | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $runRoot 'secrets/live-credentials.json')
        $roleOutput = (& (Join-Path $runRoot 'tools/role-setup.exe') -owner-uid $credentials.ADMIN.uid -activate -confirm ACTIVATE-ROLES-V1 -chat-master-key-file (Join-Path $runRoot 'server/data/keys/chat_master.key') 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) { throw 'Disposable roles-v1 activation failed; credential-bearing output suppressed.' }
    } finally {
        $env:NOXA_DATABASE_URL = $previousDatabase
        $env:NOXA_CHAT_MASTER_KEY = $previousChatKey
    }
    $serverRecord = Start-RecordedProcess 'server' (Join-Path $runRoot 'bin/server.exe') (Join-Path $runRoot 'server') $serverEnv
    Wait-Ready {
        if ($null -eq (Get-RecordedProcess $serverRecord)) { throw 'Server exited before readiness.' }
        try { return (Invoke-WebRequest "http://127.0.0.1:$($BasePort + 3)/readyz" -TimeoutSec 2).StatusCode -eq 200 }
        catch { return $false }
    } 'noXa server readiness'
    $certificate = [Security.Cryptography.X509Certificates.X509Certificate2]::CreateFromPem((Get-Content -LiteralPath (Join-Path $runRoot 'server/data/tls/cert.pem') -Raw))
    try { $fingerprint = ([Convert]::ToHexString($certificate.GetCertHash([Security.Cryptography.HashAlgorithmName]::SHA256)) -split '(..)' | Where-Object { $_ }) -join ':' }
    finally { $certificate.Dispose() }
    $testEnvironment = @{ NOXA_LIVE_ADDR = $session.address; NOXA_LIVE_TLS_FINGERPRINT = $fingerprint }
    foreach ($account in $credentials.Keys) {
        $testEnvironment["NOXA_LIVE_${account}_UID"] = $credentials[$account].uid
        $testEnvironment["NOXA_LIVE_${account}_PASS"] = $credentials[$account].password
    }
    $testEnvironment | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $runRoot 'secrets/live-environment.json')
    if (-not $NoClients) {
        foreach ($role in @('ALPHA', 'BRAVO', 'CHARLIE')) {
            $profile = Join-Path $runRoot "profiles/$role"
            foreach ($directory in @('appdata', 'localappdata', 'temp', 'webview', 'install', 'fixtures')) {
                New-Item -ItemType Directory -Path (Join-Path $profile $directory) -Force | Out-Null
            }
            $executable = Join-Path $profile "install/noXa-$role.exe"
            Copy-Item -LiteralPath (Join-Path $runRoot 'bin/client.exe') -Destination $executable
            $clientEnv = Get-CleanEnvironment
            $clientEnv.APPDATA = Join-Path $profile 'appdata'
            $clientEnv.LOCALAPPDATA = Join-Path $profile 'localappdata'
            $clientEnv.TEMP = Join-Path $profile 'temp'
            $clientEnv.TMP = $clientEnv.TEMP
            $clientEnv.WEBVIEW2_USER_DATA_FOLDER = Join-Path $profile 'webview'
            $session.profiles += [pscustomobject]@{ name = $role; root = $profile; environment = $clientEnv }
            Start-RecordedProcess $role $executable (Join-Path $profile 'install') $clientEnv -Visible | Out-Null
        }
    }
    $session.state = 'ready'
    Save-Session
    Write-Output "Ready at $($session.address). Evidence: $runRoot/evidence. Roles-v1 is active; disposable credentials are in the protected secrets directory. Run the test action for live backend checks."
} catch {
    $failure = $_
    try { Stop-Session } catch { Write-Warning "Cleanup incomplete: $($_.Exception.Message)" }
    throw $failure
} finally { Set-Location -LiteralPath $savedLocation }

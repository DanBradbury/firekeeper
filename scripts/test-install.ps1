<#
Tests install.ps1 against a fake local release. No internet.

  pwsh -NoProfile -File scripts/test-install.ps1

Runs on Windows PowerShell 5.1 and PowerShell 7, on Windows (where CI runs it)
and elsewhere. Checks that touch Windows itself, such as replacing a running
program and editing the user PATH, run only on Windows; the PATH check also
only in CI, since it changes the real user environment.
#>
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$installer = Join-Path (Split-Path -Parent $here) 'install.ps1'
$work = Join-Path ([System.IO.Path]::GetTempPath()) ('fk-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null

$onWindows = ($PSVersionTable.PSEdition -ne 'Core') -or $IsWindows
$powershell = (Get-Process -Id $PID).Path
$script:fails = 0
$script:server = $null

function Ok([string]$Name) { Write-Host "ok   $Name" }
function Bad([string]$Name) { Write-Host "FAIL $Name"; $script:fails++ }
function Check([bool]$Condition, [string]$Name) { if ($Condition) { Ok $Name } else { Bad $Name } }

# Run-Installer runs install.ps1 in a fresh PowerShell so exit codes, errors
# and environment variables behave as for a user. It returns the exit code
# and the combined output.
function Run-Installer([hashtable]$Environment, [string[]]$Arguments = @()) {
    $saved = @{}
    foreach ($key in $Environment.Keys) {
        $saved[$key] = [Environment]::GetEnvironmentVariable($key)
        [Environment]::SetEnvironmentVariable($key, $Environment[$key])
    }
    # Windows PowerShell 5.1 turns a native command's stderr into error
    # records, which $ErrorActionPreference = 'Stop' would throw. The
    # installer is meant to fail on some of these runs, so stderr is data.
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $lines = & $powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $installer @Arguments 2>&1 | ForEach-Object { "$_" }
        $code = $LASTEXITCODE
        return @{ Code = $code; Output = ($lines -join "`n") }
    }
    finally {
        $ErrorActionPreference = $savedPreference
        foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key]) }
    }
}

# New-Release lays out DIR like GitHub's releases/download/<tag>/: a zip
# holding firekeeper.exe, and checksums.txt.
function New-Release([string]$Root, [string]$Tag, [string]$Content) {
    $ver = $Tag.TrimStart('v')
    $dir = Join-Path $Root "download/$Tag"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $stage = Join-Path $work ([guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $stage | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $stage 'firekeeper.exe'), $Content)
    [System.IO.File]::WriteAllText((Join-Path $stage 'README.md'), 'readme')
    $arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { 'arm64' } else { 'amd64' }
    $name = "firekeeper_${ver}_windows_${arch}.zip"
    $zip = Join-Path $dir $name
    Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zip -Force
    $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    $other = '0' * 64
    Set-Content -LiteralPath (Join-Path $dir 'checksums.txt') -Value @("$hash  $name", "$other  firekeeper_${ver}_other_other.zip")
    return $zip
}

function ToFileUrl([string]$Path) {
    $full = [System.IO.Path]::GetFullPath($Path)
    # A rooted Unix path is a relative URI to .NET; give it its scheme by hand.
    if ($full.StartsWith('/')) { return "file://$full" }
    return ([System.Uri]$full).AbsoluteUri
}

try {
    $releases = Join-Path $work 'releases'
    New-Release $releases 'v1.2.3' 'firekeeper 1.2.3' | Out-Null
    $base = ToFileUrl $releases

    # 1. Install a pinned version.
    $dir = Join-Path $work 'bin1'
    $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.3')
    $installed = Join-Path $dir 'firekeeper.exe'
    Check (($r.Code -eq 0) -and (Test-Path $installed) -and ((Get-Content -Raw $installed) -eq 'firekeeper 1.2.3')) "install pinned version ($($r.Output.Trim()))"
    Check ($r.Output -match 'Left PATH alone') 'PATH hint printed when PATH is not modified'
    Check (-not (Test-Path (Join-Path $dir '.firekeeper.new.exe'))) 'no staging file left behind'

    # 2. A bare version, and the version from the environment.
    $dir = Join-Path $work 'bin2'
    $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1'; FIREKEEPER_VERSION = '1.2.3' }
    Check (($r.Code -eq 0) -and (Test-Path (Join-Path $dir 'firekeeper.exe'))) 'bare version from FIREKEEPER_VERSION accepted'

    # 3. A checksum mismatch aborts and installs nothing.
    $bad = Join-Path $work 'bad'
    $zip = New-Release $bad 'v1.2.3' 'firekeeper 1.2.3'
    [System.IO.File]::AppendAllText($zip, 'tampered')
    $dir = Join-Path $work 'bin3'
    $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = (ToFileUrl $bad); FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.3')
    Check (($r.Code -ne 0) -and ($r.Output -match 'checksum mismatch') -and -not (Test-Path (Join-Path $dir 'firekeeper.exe'))) 'checksum mismatch aborts, nothing installed'

    # 4. A mismatch must not clobber an existing install.
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $dir 'firekeeper.exe'), 'old')
    Run-Installer @{ FIREKEEPER_RELEASES_URL = (ToFileUrl $bad); FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.3') | Out-Null
    Check ((Get-Content -Raw (Join-Path $dir 'firekeeper.exe')) -eq 'old') 'mismatch leaves existing binary alone'

    # 5. A missing checksum entry aborts.
    $noentry = Join-Path $work 'noentry'
    New-Release $noentry 'v1.2.3' 'x' | Out-Null
    Set-Content -LiteralPath (Join-Path $noentry 'download/v1.2.3/checksums.txt') -Value ('0' * 64 + '  other.zip')
    $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = (ToFileUrl $noentry); FIREKEEPER_INSTALL_DIR = (Join-Path $work 'bin4'); FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.3')
    Check (($r.Code -ne 0) -and ($r.Output -match 'no entry')) 'missing checksum entry aborts'

    # 6. A missing release fails clearly.
    $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = (Join-Path $work 'bin5'); FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v9.9.9')
    Check (($r.Code -ne 0) -and ($r.Output -match 'could not download')) 'missing release fails clearly'

    # 7. Bad version strings are rejected before any request.
    foreach ($version in @('v1/../x', 'v1 2', 'v1;calc')) {
        $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = (Join-Path $work 'bin6'); FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', $version)
        Check (($r.Code -ne 0) -and ($r.Output -match 'invalid version') -and -not (Test-Path (Join-Path $work 'bin6'))) "invalid version rejected: $version"
    }

    # 8. Reinstalling replaces the binary and leaves no .old file when nothing runs it.
    $dir = Join-Path $work 'bin7'
    Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.3') | Out-Null
    New-Release $releases 'v1.2.4' 'firekeeper 1.2.4' | Out-Null
    $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.4')
    Check (($r.Code -eq 0) -and ((Get-Content -Raw (Join-Path $dir 'firekeeper.exe')) -eq 'firekeeper 1.2.4') -and -not (Test-Path (Join-Path $dir 'firekeeper.exe.old'))) 'upgrade replaces the binary and cleans up'

    # 9. The latest release is found through GitHub's redirect.
    $listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    $script:server = Start-Job -ArgumentList $port, $releases -ScriptBlock {
        param($port, $root)
        $http = New-Object System.Net.HttpListener
        $http.Prefixes.Add("http://127.0.0.1:$port/")
        $http.Start()
        while ($http.IsListening) {
            $context = $http.GetContext()
            $path = $context.Request.Url.AbsolutePath
            if ($path -eq '/latest') {
                $context.Response.StatusCode = 302
                $context.Response.RedirectLocation = '/tag/v1.2.4'
            }
            elseif ($path -eq '/tag/v1.2.4') {
                $context.Response.StatusCode = 200
            }
            elseif ($path.StartsWith('/download/')) {
                $file = Join-Path $root $path.Substring(1)
                if (Test-Path -LiteralPath $file) {
                    $bytes = [System.IO.File]::ReadAllBytes($file)
                    $context.Response.OutputStream.Write($bytes, 0, $bytes.Length)
                }
                else { $context.Response.StatusCode = 404 }
            }
            else { $context.Response.StatusCode = 404 }
            $context.Response.Close()
        }
    }
    # Wait for the server to accept connections.
    for ($i = 0; $i -lt 50; $i++) {
        try { $probe = New-Object System.Net.Sockets.TcpClient('127.0.0.1', $port); $probe.Close(); break } catch { Start-Sleep -Milliseconds 100 }
    }
    $dir = Join-Path $work 'bin8'
    $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = "http://127.0.0.1:$port"; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1'; FIREKEEPER_VERSION = '' }
    Check (($r.Code -eq 0) -and ((Get-Content -Raw (Join-Path $dir 'firekeeper.exe')) -eq 'firekeeper 1.2.4')) "latest release resolved through the redirect ($($r.Output.Trim()))"

    if ($onWindows) {
        # 10. A running firekeeper.exe can be upgraded: Windows renames, but will not overwrite, a running program.
        $dir = Join-Path $work 'bin9'
        New-Item -ItemType Directory -Path $dir | Out-Null
        $running = Join-Path $dir 'firekeeper.exe'
        Copy-Item (Join-Path $env:SystemRoot 'System32\ping.exe') $running
        $process = Start-Process -FilePath $running -ArgumentList '-t', '127.0.0.1' -PassThru -WindowStyle Hidden
        try {
            $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.4')
            Check (($r.Code -eq 0) -and ((Get-Content -Raw $running) -eq 'firekeeper 1.2.4') -and -not $process.HasExited) "upgrade while firekeeper.exe is running ($($r.Output.Trim()))"
        }
        finally {
            if (-not $process.HasExited) { $process.Kill() }
            $process.WaitForExit()
        }
        # The next install removes the leftover .old file.
        $r = Run-Installer @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '1' } @('-Version', 'v1.2.3')
        Check (($r.Code -eq 0) -and -not (Test-Path (Join-Path $dir 'firekeeper.exe.old'))) 'leftover .old file is removed by the next install'

        # 11. The install directory joins the user PATH once. This edits the
        # real user environment, so it runs only in CI.
        if ($env:CI) {
            $originalPath = [Environment]::GetEnvironmentVariable('Path', 'User')
            try {
                $dir = Join-Path $work 'bin10'
                $env10 = @{ FIREKEEPER_RELEASES_URL = $base; FIREKEEPER_INSTALL_DIR = $dir; FIREKEEPER_NO_MODIFY_PATH = '' }
                Run-Installer $env10 @('-Version', 'v1.2.3') | Out-Null
                Run-Installer $env10 @('-Version', 'v1.2.3') | Out-Null
                $entries = @(([Environment]::GetEnvironmentVariable('Path', 'User')) -split ';' | Where-Object { $_.TrimEnd('\') -ieq $dir.TrimEnd('\') })
                Check ($entries.Count -eq 1) "install directory is on the user PATH exactly once (found $($entries.Count))"
            }
            finally {
                [Environment]::SetEnvironmentVariable('Path', $originalPath, 'User')
            }
        }
    }
}
finally {
    if ($script:server) { Stop-Job $script:server -ErrorAction SilentlyContinue; Remove-Job $script:server -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}

if ($script:fails -gt 0) {
    Write-Host "$($script:fails) check(s) failed"
    exit 1
}
Write-Host 'all checks passed'

<#
.SYNOPSIS
Installs Firekeeper from a GitHub release on Windows.

.DESCRIPTION
Downloads the release zip for this machine's architecture, checks its SHA-256
against the release's checksums.txt, and installs firekeeper.exe for the
current user. It never needs administrator rights.

  irm https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.ps1 | iex

To pick a version or a directory when piping, set the variables first:

  $env:FIREKEEPER_VERSION = 'v0.1.0'; irm https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.ps1 | iex

Installs to %LOCALAPPDATA%\Programs\firekeeper and adds that directory to the
user's PATH. Open a new terminal afterwards, or use the current one, which is
updated too.

Environment:
  FIREKEEPER_VERSION       release to install, such as v0.1.0 (default: latest)
  FIREKEEPER_INSTALL_DIR   install directory (an absolute path)
  FIREKEEPER_NO_MODIFY_PATH  set to any value to leave PATH alone
  FIREKEEPER_RELEASES_URL  release location (used by the tests): https://,
                           file://, or http:// to this machine only

.PARAMETER Version
Release to install, such as v0.1.0. Defaults to the latest release.

.PARAMETER InstallDir
Directory for firekeeper.exe, as an absolute path.

.PARAMETER NoPathUpdate
Do not add the install directory to the user's PATH.
#>
[CmdletBinding()]
param(
    [string]$Version = $env:FIREKEEPER_VERSION,
    [string]$InstallDir = $env:FIREKEEPER_INSTALL_DIR,
    [switch]$NoPathUpdate
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version 2.0

function Fail([string]$Message) {
    throw "install.ps1: $Message"
}

function Get-FirekeeperArch {
    $arch = $null
    try { $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { $arch = $null }
    if (-not $arch) {
        if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 } else { $arch = $env:PROCESSOR_ARCHITECTURE }
    }
    switch -Regex ($arch) {
        '^(X64|AMD64)$' { return 'amd64' }
        '^(Arm64|ARM64)$' { return 'arm64' }
        default { Fail "unsupported architecture ${arch}: Firekeeper releases cover amd64 and arm64. See the README to build from source." }
    }
}

# Save-Url downloads with WebClient, which handles https and file:// alike,
# uses the system proxy, and avoids Invoke-WebRequest's slow progress bar on
# Windows PowerShell 5.1.
function Save-Url([string]$Url, [string]$Path) {
    $client = New-Object System.Net.WebClient
    try {
        $client.Headers['User-Agent'] = 'firekeeper-install'
        $client.Proxy = [System.Net.WebRequest]::GetSystemWebProxy()
        $client.Proxy.Credentials = [System.Net.CredentialCache]::DefaultCredentials
        $client.DownloadFile($Url, $Path)
    }
    finally {
        $client.Dispose()
    }
}

# Get-LatestTag follows GitHub's /releases/latest redirect to
# /releases/tag/<tag>, which needs no API token and has no rate limit.
function Get-LatestTag([string]$ReleasesUrl) {
    $request = [System.Net.WebRequest]::Create("$ReleasesUrl/latest")
    $request.Proxy = [System.Net.WebRequest]::GetSystemWebProxy()
    $request.Proxy.Credentials = [System.Net.CredentialCache]::DefaultCredentials
    if ($request -is [System.Net.HttpWebRequest]) { $request.UserAgent = 'firekeeper-install' }
    $response = $request.GetResponse()
    try { $final = $response.ResponseUri.AbsoluteUri } finally { $response.Close() }
    $tag = $final.TrimEnd('/').Split('/')[-1]
    if (-not $tag -or $tag -eq 'latest') { Fail 'could not work out the latest release tag' }
    return $tag
}

# New-Directory creates Path and any missing parents, taking the path
# literally (New-Item has no -LiteralPath). An existing directory is fine.
function New-Directory([string]$Path) {
    [System.IO.Directory]::CreateDirectory($Path) | Out-Null
}

# Add-ToUserPath appends Dir to the user PATH in HKCU\Environment unless it is
# already there, and returns whether it did. It reads and writes the raw value:
# [Environment]::GetEnvironmentVariable would expand entries such as
# %USERPROFILE%\bin and SetEnvironmentVariable would write them back as REG_SZ,
# freezing them. The value keeps its kind (REG_EXPAND_SZ, as Windows creates
# it, or REG_SZ); a new value is REG_EXPAND_SZ.
function Add-ToUserPath([string]$Dir) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    try {
        $current = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
        if (($key.GetValueNames() -icontains 'Path') -and $key.GetValueKind('Path') -eq [Microsoft.Win32.RegistryValueKind]::String) {
            $kind = [Microsoft.Win32.RegistryValueKind]::String
        }
        $parts = @($current.Split(';') | Where-Object { $_ })
        $wanted = $Dir.TrimEnd('\')
        foreach ($part in $parts) {
            if ($part.TrimEnd('\') -ieq $wanted) { return $false }
            if ([Environment]::ExpandEnvironmentVariables($part).TrimEnd('\') -ieq $wanted) { return $false }
        }
        $key.SetValue('Path', (($parts + $Dir) -join ';'), $kind)
    }
    finally {
        $key.Close()
    }
    Send-EnvironmentChange
    return $true
}

# Send-EnvironmentChange tells running programs, such as Explorer, that the
# user environment changed, as SetEnvironmentVariable does, so terminals they
# start next see the new PATH. It is best effort: without it, signing out and
# back in does the same.
function Send-EnvironmentChange {
    try {
        if (-not ('Firekeeper.Install.NativeMethods' -as [type])) {
            Add-Type -Namespace Firekeeper.Install -Name NativeMethods -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
        }
        $result = [UIntPtr]::Zero
        # HWND_BROADCAST, WM_SETTINGCHANGE, SMTO_ABORTIFHUNG, 5 seconds.
        [Firekeeper.Install.NativeMethods]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result) | Out-Null
    }
    catch { }
}

# The installer calls .NET directly instead of module cmdlets such as
# Get-FileHash and Expand-Archive. PowerShell loads those from PSModulePath on
# first use, and Windows PowerShell 5.1 started from PowerShell 7 inherits a
# PSModulePath that points at 7's modules, which 5.1 cannot load: the cmdlet
# is then "not recognized". Everything below uses only .NET and cmdlets built
# into both editions.

# Get-Sha256 returns the SHA-256 of the file at Path as lowercase hex.
function Get-Sha256([string]$Path) {
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    try {
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try { $bytes = $sha.ComputeHash($stream) } finally { $sha.Dispose() }
    }
    finally {
        $stream.Dispose()
    }
    return ([System.BitConverter]::ToString($bytes) -replace '-', '').ToLowerInvariant()
}

# Expand-Zip extracts the zip at Path into Destination, which must not exist
# yet. ZipFile refuses entries that would land outside Destination.
function Expand-Zip([string]$Path, [string]$Destination) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [System.IO.Compression.ZipFile]::ExtractToDirectory($Path, $Destination)
}

# Test-ReleasesUrl allows https://, file:// on this machine (the tests' fake
# release), and http:// only to this machine (the tests' redirect server).
# Anything else would fetch the zip and checksums.txt in clear text, where the
# checksum protects nothing. The scheme must be written out: .NET reads a bare
# //host/share as a file share.
function Test-ReleasesUrl([string]$Url) {
    if ($Url -notmatch '^(https|http|file)://') { return $false }
    $uri = $null
    if (-not [System.Uri]::TryCreate($Url, [System.UriKind]::Absolute, [ref]$uri)) { return $false }
    switch ($uri.Scheme) {
        'https' { return $true }
        'file' { return -not $uri.IsUnc }
        'http' { return $uri.IsLoopback }
        default { return $false }
    }
}

# Test-AbsolutePath reports whether Path names one place regardless of the
# current directory and drive: C:\dir or \\server\share on Windows, /dir
# elsewhere. A relative directory on the user PATH would resolve against each
# program's current directory, so it is refused, as are \dir and C:dir,
# which depend on the current drive or its current directory.
function Test-AbsolutePath([string]$Path) {
    if (-not $Path) { return $false }
    if ([System.IO.Path]::DirectorySeparatorChar -eq '/') { return $Path.StartsWith('/') }
    return ($Path -match '^[A-Za-z]:[\\/]') -or ($Path -match '^[\\/]{2}[^\\/]')
}

function Install-Firekeeper {
    $releasesUrl = $env:FIREKEEPER_RELEASES_URL
    if (-not $releasesUrl) {
        $releasesUrl = 'https://github.com/DanBradbury/firekeeper/releases'
        if ($PSVersionTable.PSEdition -eq 'Core' -and -not $IsWindows) {
            Fail 'this installer is for Windows. On macOS and Linux use install.sh.'
        }
    }
    $releasesUrl = $releasesUrl.TrimEnd('/')
    if (-not (Test-ReleasesUrl $releasesUrl)) {
        Fail 'FIREKEEPER_RELEASES_URL must use https:// (or file://, or http:// to this machine for tests); unset it to install from GitHub'
    }

    if (-not $InstallDir) {
        $localAppData = [Environment]::GetFolderPath('LocalApplicationData')
        if (-not $localAppData) { Fail 'cannot work out the install directory: LOCALAPPDATA is not set; set FIREKEEPER_INSTALL_DIR' }
        $InstallDir = Join-Path $localAppData 'Programs\firekeeper'
    }
    if (-not (Test-AbsolutePath $InstallDir)) {
        Fail "install directory must be an absolute path, such as C:\Tools\firekeeper (got $InstallDir); a relative one on PATH resolves against each program's current directory"
    }

    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    }
    catch { }

    $arch = Get-FirekeeperArch

    if ($Version) {
        if ($Version.StartsWith('v')) { $tag = $Version } else { $tag = "v$Version" }
    }
    else {
        try { $tag = Get-LatestTag $releasesUrl }
        catch { Fail "could not look up the latest release at $releasesUrl/latest" }
    }
    if ($tag -notmatch '^[A-Za-z0-9._-]+$') { Fail "invalid version: $tag" }
    $ver = $tag.Substring(1)
    $archive = "firekeeper_${ver}_windows_${arch}.zip"

    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('firekeeper-install-' + [System.Guid]::NewGuid().ToString('N'))
    New-Directory $tmp
    try {
        Write-Host "Installing firekeeper $tag for windows/$arch"
        $zip = Join-Path $tmp $archive
        $sums = Join-Path $tmp 'checksums.txt'
        try { Save-Url "$releasesUrl/download/$tag/$archive" $zip }
        catch { Fail "could not download $archive for $tag (does that release exist?)" }
        try { Save-Url "$releasesUrl/download/$tag/checksums.txt" $sums }
        catch { Fail "could not download checksums.txt for $tag" }

        $want = $null
        foreach ($line in [System.IO.File]::ReadAllLines($sums)) {
            $fields = $line.Trim() -split '\s+'
            if ($fields.Count -ge 2 -and $fields[1].TrimStart('*') -eq $archive) { $want = $fields[0].ToLowerInvariant() }
        }
        if (-not $want) { Fail "checksums.txt has no entry for $archive" }
        $got = Get-Sha256 $zip
        if ($got -ne $want) {
            Fail "checksum mismatch for $archive (expected $want, got $got); nothing was installed"
        }

        $extracted = Join-Path $tmp 'x'
        try { Expand-Zip $zip $extracted }
        catch { Fail "could not extract firekeeper.exe from $archive" }
        $exe = Join-Path $extracted 'firekeeper.exe'
        if (-not [System.IO.File]::Exists($exe)) { Fail "could not extract firekeeper.exe from $archive" }

        try { New-Directory $InstallDir }
        catch { Fail "could not create $InstallDir; set FIREKEEPER_INSTALL_DIR to a writable directory" }
        $target = Join-Path $InstallDir 'firekeeper.exe'
        $staged = Join-Path $InstallDir '.firekeeper.new.exe'
        $previous = "$target.old"
        try { Copy-Item -LiteralPath $exe -Destination $staged -Force }
        catch { Fail "could not write to $InstallDir; set FIREKEEPER_INSTALL_DIR to a writable directory" }

        # Windows will not overwrite a running program, but it will rename one.
        # Moving the old binary aside lets an update run from firekeeper
        # itself, and a failed install put it back.
        if (Test-Path -LiteralPath $previous) { Remove-Item -LiteralPath $previous -Force -ErrorAction SilentlyContinue }
        $hadPrevious = Test-Path -LiteralPath $target
        if ($hadPrevious) { Move-Item -LiteralPath $target -Destination $previous -Force }
        try { Move-Item -LiteralPath $staged -Destination $target -Force }
        catch {
            if ($hadPrevious) { Move-Item -LiteralPath $previous -Destination $target -Force -ErrorAction SilentlyContinue }
            Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
            Fail "could not install $target"
        }
        # Still locked while the old firekeeper.exe runs; the next install removes it.
        if ($hadPrevious) { Remove-Item -LiteralPath $previous -Force -ErrorAction SilentlyContinue }
    }
    finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host "Installed $target"

    if ($NoPathUpdate -or $env:FIREKEEPER_NO_MODIFY_PATH) {
        Write-Host "Left PATH alone. Add $InstallDir to it to run firekeeper from anywhere."
        return
    }
    if (Add-ToUserPath $InstallDir) {
        Write-Host "Added $InstallDir to your user PATH."
    }
    # Make the current session see it too, so the next command just works.
    $inSession = @($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') })
    if ($inSession.Count -eq 0) { $env:Path = "$env:Path;$InstallDir" }
    Write-Host ''
    Write-Host 'Run firekeeper to open the dashboard. New terminals pick up the PATH change on their own.'
}

Install-Firekeeper

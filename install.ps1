[CmdletBinding()]
param(
    [string]$Version = $env:TRAJECTOR_VERSION,
    [string]$InstallDir = $env:TRAJECTOR_INSTALL_DIR
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http
Add-Type -AssemblyName System.IO.Compression

function Assert-DownloadUrl([Uri]$Url) {
    if (-not $Url.IsAbsoluteUri -or $Url.UserInfo -or $Url.Fragment) {
        throw 'A download URL must be absolute and contain no credentials or fragment.'
    }
    if ($Url.Scheme -ne 'https' -and -not ($Url.Scheme -eq 'http' -and $Url.Host -in @('127.0.0.1', '[::1]', 'localhost'))) {
        throw 'Downloads require HTTPS; HTTP is allowed only on loopback for testing.'
    }
}

function Get-DownloadBytes([Uri]$Url, [long]$Limit) {
    $handler = New-Object System.Net.Http.HttpClientHandler
    $handler.AllowAutoRedirect = $false
    $client = New-Object System.Net.Http.HttpClient($handler)
    $client.DefaultRequestHeaders.UserAgent.ParseAdd('trajector-windows-installer')
    $cancel = New-Object System.Threading.CancellationTokenSource
    $cancel.CancelAfter([TimeSpan]::FromMinutes(5))
    try {
        for ($hop = 0; $hop -le 10; $hop++) {
            Assert-DownloadUrl $Url
            $request = New-Object System.Net.Http.HttpRequestMessage([System.Net.Http.HttpMethod]::Get, $Url)
            $response = $null
            try {
                $response = $client.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $cancel.Token).GetAwaiter().GetResult()
                if ([int]$response.StatusCode -in @(301, 302, 303, 307, 308)) {
                    if (-not $response.Headers.Location) { throw 'The release source returned a redirect without a destination.' }
                    $Url = New-Object Uri($Url, $response.Headers.Location)
                    continue
                }
                if (-not $response.IsSuccessStatusCode) { throw "The release source returned HTTP $([int]$response.StatusCode)." }
                if ($response.Content.Headers.ContentLength -gt $Limit) { throw 'The download exceeds its size limit.' }
                $stream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
                $buffer = New-Object byte[] 65536
                $output = New-Object System.IO.MemoryStream
                try {
                    while (($count = $stream.ReadAsync($buffer, 0, $buffer.Length, $cancel.Token).GetAwaiter().GetResult()) -gt 0) {
                        if ($output.Length + $count -gt $Limit) { throw 'The download exceeds its size limit.' }
                        $output.Write($buffer, 0, $count)
                    }
                    return ,$output.ToArray()
                } finally {
                    $output.Dispose()
                    $stream.Dispose()
                }
            } finally {
                if ($response) { $response.Dispose() }
                $request.Dispose()
            }
        }
        throw 'The release source redirected too many times.'
    } finally {
        $cancel.Dispose()
        $client.Dispose()
        $handler.Dispose()
    }
}

function Get-ReleaseVersion([string]$Tag) {
    if ($Tag -cnotmatch '^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$') { return $null }
    $parts = @($Matches[1], $Matches[2], $Matches[3])
    $pre = @()
    if ($Matches.ContainsKey(4) -and $Matches[4]) { $pre = @($Matches[4].Split('.')) }
    foreach ($part in $pre) {
        if ($part -match '^0[0-9]+$') { return $null }
    }
    return @{ Core = $parts; Pre = $pre; Text = $Tag.TrimStart('v') }
}

function Compare-NumericPart([string]$Left, [string]$Right) {
    if ($Left.Length -ne $Right.Length) { return $Left.Length.CompareTo($Right.Length) }
    return [string]::CompareOrdinal($Left, $Right)
}

function Compare-ReleaseVersion($Left, $Right) {
    for ($i = 0; $i -lt 3; $i++) {
        $order = Compare-NumericPart $Left.Core[$i] $Right.Core[$i]
        if ($order -ne 0) { return $order }
    }
    if ($Left.Pre.Count -eq 0 -or $Right.Pre.Count -eq 0) { return $Right.Pre.Count.CompareTo($Left.Pre.Count) }
    for ($i = 0; $i -lt [Math]::Min($Left.Pre.Count, $Right.Pre.Count); $i++) {
        $a = $Left.Pre[$i]
        $b = $Right.Pre[$i]
        $aNumeric = $a -match '^[0-9]+$'
        $bNumeric = $b -match '^[0-9]+$'
        if ($aNumeric -and $bNumeric) { $order = Compare-NumericPart $a $b }
        elseif ($aNumeric -ne $bNumeric) { $order = $bNumeric.CompareTo($aNumeric) }
        else { $order = [string]::CompareOrdinal($a, $b) }
        if ($order -ne 0) { return $order }
    }
    return $Left.Pre.Count.CompareTo($Right.Pre.Count)
}

$staged = $null
try {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Use install.sh on macOS and Linux.' }
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
    switch ($architecture) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "Unsupported architecture: $architecture. Windows builds require amd64 or arm64." }
    }
    if (-not $InstallDir) { $InstallDir = Join-Path $env:USERPROFILE '.local\bin' }
    $InstallDir = [IO.Path]::GetFullPath($InstallDir)
    $destination = Join-Path $InstallDir 'trajector.exe'
    if (Test-Path -LiteralPath $destination) { throw 'trajector.exe is already installed here. Run trajector upgrade to update it with rollback protection.' }

    $api = 'https://api.github.com'
    $downloads = 'https://github.com/PublicAI01/trajector-cli/releases/download'
    if ($env:TRAJECTOR_API_BASE) { $api = $env:TRAJECTOR_API_BASE.TrimEnd('/') }
    if ($env:TRAJECTOR_DL_BASE) { $downloads = $env:TRAJECTOR_DL_BASE.TrimEnd('/') }
    $best = $null
    $tag = $null
    if ($Version) {
        $best = Get-ReleaseVersion $Version
        if (-not $best) { throw 'The requested version is not a semantic version.' }
        $tag = 'v' + $best.Text
    } else {
        $indexBytes = Get-DownloadBytes "$api/repos/PublicAI01/trajector-cli/releases" (8MB)
        $releases = [Text.Encoding]::UTF8.GetString($indexBytes) | ConvertFrom-Json
        foreach ($release in $releases) {
            if ($release.draft) { continue }
            $candidate = Get-ReleaseVersion $release.tag_name
            if (-not $candidate) { continue }
            if (-not $best -or (Compare-ReleaseVersion $candidate $best) -gt 0) {
                $best = $candidate
                $tag = $release.tag_name
                $assets = @($release.assets | ForEach-Object { $_.name })
            }
        }
        if (-not $best) { throw 'No usable release is published.' }
        if ("trajector_$($best.Text)_windows_$arch.zip" -cnotin $assets -or 'trajector_checksums.txt' -cnotin $assets) {
            throw 'The newest release has no Windows archive and checksum for this architecture; nothing was installed.'
        }
    }
    $archiveName = "trajector_$($best.Text)_windows_$arch.zip"
    $releaseUrl = "$downloads/$([Uri]::EscapeDataString($tag))"
    $sums = [Text.Encoding]::UTF8.GetString((Get-DownloadBytes "$releaseUrl/trajector_checksums.txt" (1MB)))
    $hashes = @()
    foreach ($line in $sums.Split("`n")) {
        if ($line.TrimEnd("`r") -cmatch ('^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($archiveName) + '$')) { $hashes += $Matches[1] }
    }
    if ($hashes.Count -ne 1) { throw 'The release must publish exactly one SHA-256 checksum for this archive.' }
    $archive = Get-DownloadBytes "$releaseUrl/$archiveName" (256MB)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $actual = [BitConverter]::ToString($sha.ComputeHash($archive)).Replace('-', '') }
    finally { $sha.Dispose() }
    if ($actual -ine $hashes[0]) { throw 'The archive does not match its published SHA-256 checksum; nothing was installed.' }

    $archiveStream = New-Object IO.MemoryStream(,$archive)
    $zip = New-Object IO.Compression.ZipArchive($archiveStream, [IO.Compression.ZipArchiveMode]::Read)
    try {
        $entries = @($zip.Entries | Where-Object { $_.FullName -ceq 'trajector.exe' })
        if ($entries.Count -ne 1 -or $entries[0].Length -le 0 -or $entries[0].Length -gt 256MB) { throw 'The archive must contain exactly one nonempty trajector.exe within the size limit.' }
        [void][IO.Directory]::CreateDirectory($InstallDir)
        $staged = Join-Path $InstallDir ('.trajector-install-' + [Guid]::NewGuid().ToString('N') + '.exe')
        $source = $entries[0].Open()
        $target = New-Object IO.FileStream($staged, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try {
            $buffer = New-Object byte[] 65536
            while (($count = $source.Read($buffer, 0, $buffer.Length)) -gt 0) {
                if ($target.Length + $count -gt $entries[0].Length) { throw 'The archive entry exceeds its declared size.' }
                $target.Write($buffer, 0, $count)
            }
            if ($target.Length -ne $entries[0].Length) { throw 'The archive entry is truncated.' }
            $target.Flush($true)
        } finally {
            $target.Dispose()
            $source.Dispose()
        }
    } finally {
        $zip.Dispose()
        $archiveStream.Dispose()
    }
    $start = New-Object Diagnostics.ProcessStartInfo
    $start.FileName = $staged
    $start.Arguments = 'version'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $process = [Diagnostics.Process]::Start($start)
    try {
        $reported = $process.StandardOutput.ReadToEndAsync()
        if (-not $process.WaitForExit(10000)) {
            $process.Kill()
            $process.WaitForExit()
            throw 'The downloaded binary did not answer its version check.'
        }
        if ($process.ExitCode -ne 0 -or $reported.GetAwaiter().GetResult().TrimEnd("`r", "`n") -cne "trajector $($best.Text)") { throw 'The downloaded binary does not report the requested version.' }
    } finally { $process.Dispose() }
    [IO.File]::Move($staged, $destination)
    $staged = $null
    Write-Output "Installed trajector $($best.Text) in $InstallDir"
    Write-Output 'Run trajector enable from a project directory. Add the installation directory to your user PATH if needed.'
} catch {
    [Console]::Error.WriteLine('install.ps1: ' + $_.Exception.Message)
    exit 1
} finally {
    if ($staged -and (Test-Path -LiteralPath $staged)) { Remove-Item -LiteralPath $staged -Force }
}

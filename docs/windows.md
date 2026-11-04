# Native Windows

Trajector runs as a per-user command and an on-demand background proxy. It
does not install a Windows service, need administrator rights, or require
WSL. Keep Claude Code and Trajector on the same side of a WSL boundary.

## Installation and upgrades

Use `install.ps1` from PowerShell 5.1 or PowerShell 7. The newest release must carry a matching Windows archive: the installer
never silently falls back to the unsupported legacy Windows builds.
Windows amd64 and arm64 release archives are ZIP files, listed in `trajector_checksums.txt`
and covered by the release workflow's provenance attestation. The installer
verifies the checksum before extracting one executable and checks its
reported version before placing it at the destination. It rejects an
existing executable; use `trajector upgrade` for upgrades, which stages,
checks, replaces and verifies the new version with rollback on failure.
Custom source builds keep automatic upgrades disabled.

The installer leaves PATH and permanent execution-policy settings alone.
A source build uses `.\scripts\build.ps1`; it needs the Go toolchain in
`go.mod`. `-Version` is useful for a source archive without Git metadata;
it does not mark the build as an official release.

Use current Claude Code with [PowerShell command-hook support](https://code.claude.com/docs/en/hooks#command-hook-fields). Trajector
sets `shell: powershell` on its own hooks and quotes executable paths as
literal strings. Git snapshots require Git for Windows on PATH. The proxy
and its supervised worker run without creating a console window.

## Private state

| Content | Default directory |
| --- | --- |
| Configuration, consent, routing and file-backed credentials | `%APPDATA%\trajector` |
| Upload queue, cursors and upload metadata | `%LOCALAPPDATA%\trajector` |
| Proxy and reader logs | `%LOCALAPPDATA%\trajector` |

Private state directories use protected, inheritable Windows access control
lists granting access to the current user, SYSTEM and local administrators.
POSIX mode bits alone are not used as evidence of Windows privacy. Use a
filesystem that supports Windows ACLs; a failure to establish them is an
error. Atomic state writes protect temporary files before writing any content.
Ordinary edits retain existing Windows file ACLs; writing a consent token
to project settings restricts that file to the same private principals.
All three XDG overrides can relocate state for isolated testing.

Project identity resolves long names, casing and symbolic links through
`filepath.EvalSymlinks`. Discovery starts at the project's drive or UNC
share root. Session cursors retain a volume-qualified Windows file ID,
including the 128-bit ID when the filesystem supports it, so replacing a
file without making it smaller restarts reading with the existing message
ID deduplication. `file_id` is optional local cursor metadata: older cursors
continue from their saved position and acquire it on the next read. An older
client ignores it and has its previous replacement-detection limitations.
The upload envelope, consent boundary and acknowledgement rules are unchanged.

## Diagnostics

Run `trajector status` and `trajector doctor bundle` inside an enabled
project. A bundle contains diagnostic metadata and local project paths;
review it before sharing. Never attach the raw queue or credentials.

For queue sizes by date, without opening record contents:

```powershell
$queue = Join-Path $env:LOCALAPPDATA 'trajector\rawcalls'
Get-ChildItem -LiteralPath $queue -Directory | ForEach-Object {
    $files = @(Get-ChildItem -LiteralPath $_.FullName -File -Recurse)
    [pscustomobject]@{
        Folder = $_.Name
        Files = $files.Count
        Bytes = ($files | Measure-Object Length -Sum).Sum
    }
}
```

For upload log lines, review paths and addresses before sharing:

```powershell
Select-String -LiteralPath (Join-Path $env:LOCALAPPDATA 'trajector\proxy.log') -Pattern 'flush|upload' |
    Select-Object -Last 2000 -ExpandProperty Line
```

Do not clear the queue or restart the proxy merely to collect diagnostics.

## Verification

The Windows CI job runs the same functional session-file, Git, CLI and
proxy tests as Unix. Additional native tests cover protected directory
permissions, file replacement and cursor persistence, path identity, and
the real PowerShell installer against an isolated release server. Installer
tests run under both installed PowerShell editions. Symbolic-link tests
skip only when Windows refuses creation for lack of the required privilege;
Developer Mode or the symbolic-link privilege is needed to exercise them.
Tests asserting POSIX mode-bit behavior remain platform-specific.

```powershell
$env:GOTOOLCHAIN = 'go1.26.10'
go test ./...
go vet ./...
.\scripts\build.ps1 -Out .\dist\trajector.exe
```

All service tests use local stand-ins and isolated user directories. Before
publishing a supported Windows release, maintainers should also verify
login, enable, an actual Claude Code session, acknowledged upload, doctor,
upgrade and rollback, and disable against their service on the architectures
they support. Cross-compilation does not establish runtime ARM64 coverage.

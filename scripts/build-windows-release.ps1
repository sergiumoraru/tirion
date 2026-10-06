param(
    [Parameter(Mandatory = $false)]
    [string]$Version = "dev",

    [Parameter(Mandatory = $false)]
    [string]$DatabaseUrl = "",

    [Parameter(Mandatory = $false)]
    [string]$OutputDir = "dist\windows-amd64-mcp",

    [Parameter(Mandatory = $false)]
    [switch]$SkipDbCheck
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Resolve-Path (Join-Path $scriptDir "..")
Set-Location $repoRoot

function Fail {
    param([string]$msg)
    Write-Host "ERROR: $msg" -ForegroundColor Red
    exit 1
}

if ($Version -notmatch '^[A-Za-z0-9][A-Za-z0-9._+-]*$') {
    Fail "Version must not contain path separators or whitespace."
}

# Only generated directories beneath this checkout's dist may be replaced.
$distRoot = [System.IO.Path]::GetFullPath((Join-Path $repoRoot "dist"))
$outputPath = if ([System.IO.Path]::IsPathRooted($OutputDir)) {
    [System.IO.Path]::GetFullPath($OutputDir)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $repoRoot $OutputDir))
}
if (-not $outputPath.StartsWith($distRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
    Fail "OutputDir must be a generated subdirectory of $distRoot."
}
$ancestor = $outputPath
while ($ancestor -and $ancestor -ne [string]$repoRoot) {
    if (Test-Path -LiteralPath $ancestor) {
        $item = Get-Item -LiteralPath $ancestor -Force
        if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
            Fail "OutputDir must not traverse a symbolic link or junction: $ancestor"
        }
    }
    $ancestor = Split-Path -Parent $ancestor
}
$OutputDir = $outputPath

function Resolve-CommandPath {
    param(
        [string]$name
    )

    $cmd = Get-Command $name -ErrorAction SilentlyContinue
    if ($cmd) {
        return $cmd.Source
    }
    return $null
}

function Resolve-Gcc {
    $pathsToCheck = @(@(
        (Resolve-CommandPath "x86_64-w64-mingw32-gcc.exe"),
        (Resolve-CommandPath "gcc.exe")
    ) | Where-Object { $_ } | Select-Object -Unique)

    if ($pathsToCheck.Count -gt 0) {
        return $pathsToCheck[0]
    }

    if ($env:LOCALAPPDATA) {
        $packagesRoot = Join-Path $env:LOCALAPPDATA "Microsoft\WinGet\Packages"
        if (Test-Path $packagesRoot) {
            try {
                $searchRoot = Get-ChildItem $packagesRoot -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -like "*MartinStorsjo.LLVM-MinGW*" }
                foreach ($r in $searchRoot) {
                    $match = Get-ChildItem (Join-Path $r.FullName "*") -Recurse -Filter "x86_64-w64-mingw32-gcc.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
                    if ($match) {
                        return $match.FullName
                    }
                }
            } catch {
                # ignore package scan failures and continue to explicit installation checks
            }
        }
    }

    return $null
}

function Check-Db {
    param([string]$dbUrl)

    if ($SkipDbCheck) {
        Write-Host "DB check skipped by -SkipDbCheck." -ForegroundColor Yellow
        return
    }

    if ([string]::IsNullOrWhiteSpace($dbUrl)) {
        $dbUrl = $env:DATABASE_URL
    }

    if ([string]::IsNullOrWhiteSpace($dbUrl)) {
        Fail "DATABASE_URL is required for DB check. Pass -DatabaseUrl or set `$env:DATABASE_URL."
    }

    try {
        $uri = [System.Uri]$dbUrl
    } catch {
        Fail "Invalid DATABASE_URL. Expected a postgres URL like postgres://user:pass@host:5432/tirion?sslmode=disable"
    }

    if ($uri.Scheme -ne "postgres" -and $uri.Scheme -ne "postgresql") {
        Fail "Database scheme must be postgres:// or postgresql://"
    }

    $dbHost = $uri.Host
    Write-Host "Checking database connectivity to $dbHost"
    $port = if ($uri.Port -gt 0) { $uri.Port } else { 5432 }
    $tcp = Test-NetConnection -ComputerName $dbHost -Port $port -WarningAction SilentlyContinue
    if (-not $tcp.TcpTestSucceeded) {
        Fail "Postgres port check failed: could not reach $dbHost`:$port"
    }

    $psql = Resolve-CommandPath "psql.exe"
    if ($psql) {
        Write-Host "Running `SELECT 1` via psql..."
        & $psql -d $dbUrl -c 'SELECT 1;' | Out-Host
        if ($LASTEXITCODE -ne 0) {
            Fail "PostgreSQL auth/query check failed. Check username/password and DB name."
        }
    } else {
        Write-Host "psql not found; skipped SQL auth test. TCP connectivity passed." -ForegroundColor Yellow
    }

    $env:DATABASE_URL = $dbUrl
    Write-Host "Database check passed." -ForegroundColor Green
}

function Build {
    param(
        [string]$name,
        [string]$outFile,
        [string]$ldflags
    )

    Write-Host "  Building $name..."
    & "go" build -trimpath -ldflags="$ldflags" -o $outFile "./cmd/$name"
    if ($LASTEXITCODE -ne 0) {
        Fail "Build failed for $name"
    }
}

$go = Resolve-CommandPath "go"
if (-not $go) {
    Fail "go not found in PATH."
}
$goVersion = & $go version
Write-Host "Go: $goVersion"

$gcc = Resolve-Gcc
if (-not $gcc) {
    Fail "No GCC found. Install one of: MartinStorsjo.LLVM-MinGW.UCRT or BrechtSanders WinLibs, then rerun."
}

$gpp = $null
$gccDir = Split-Path $gcc -Parent
$gpp = Join-Path $gccDir "x86_64-w64-mingw32-g++.exe"
if (-not (Test-Path $gpp)) {
    $gpp = Join-Path $gccDir "g++.exe"
}

$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "1"
$env:PATH = "${gccDir};$env:PATH"
$env:CC = $gcc
if (Test-Path $gpp) {
    $env:CXX = $gpp
}

$tmpRoot = Join-Path (Resolve-Path .) "tmp\windows-release"
$env:GOCACHE = Join-Path $tmpRoot "gocache"
$env:GOMODCACHE = Join-Path $tmpRoot "gomodcache"
$env:GOTMPDIR = Join-Path $tmpRoot "gotmp"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOMODCACHE, $env:GOTMPDIR | Out-Null

Write-Host "Building Windows MCP with standard go build." -ForegroundColor Yellow

$commands = @(
    "mcp-intel",
    "mcp-guard"
)

if (-not $SkipDbCheck -and -not [string]::IsNullOrWhiteSpace($DatabaseUrl)) {
    Check-Db -dbUrl $DatabaseUrl
}

if (Test-Path $OutputDir) { Remove-Item -Recurse -Force $OutputDir }
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

$module = & $go list -m
if ($LASTEXITCODE -ne 0) { Fail "Could not read Go module identity." }
$ldflags = "-s -w -X $module/internal/buildinfo.Version=$Version"
foreach ($cmdName in $commands) {
    $out = Join-Path $OutputDir "${cmdName}.exe"
    Build -name $cmdName -outFile $out -ldflags $ldflags
}

foreach ($document in Get-Content (Join-Path $repoRoot "scripts/release-documents.txt")) {
    if ([string]::IsNullOrWhiteSpace($document)) { continue }
    $destination = Join-Path $OutputDir $document
    New-Item -ItemType Directory -Force -Path (Split-Path $destination -Parent) | Out-Null
    Copy-Item (Join-Path $repoRoot $document) $destination
}
foreach ($notice in @("LICENSE", "THIRD_PARTY_NOTICES.md")) {
    $source = Join-Path $repoRoot $notice
    if (-not (Test-Path $source)) { Fail "Required distribution notice is missing: $notice" }
    Copy-Item $source $OutputDir
}
if (Test-Path (Join-Path $repoRoot "NOTICE")) { Copy-Item (Join-Path $repoRoot "NOTICE") $OutputDir }

$zipName = "tirion-mcp-$Version-windows-amd64.zip"
$zipPath = Join-Path $repoRoot "dist\$zipName"
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
Compress-Archive -Path (Join-Path $OutputDir "*") -DestinationPath $zipPath -Force

Write-Host "Build complete."
Write-Host "Exes: $OutputDir"
Write-Host "Archive: $zipPath"

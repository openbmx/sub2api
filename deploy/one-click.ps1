<#
.SYNOPSIS
    Sub2API one-click deploy on Windows — builds THIS working tree and starts the stack.

.DESCRIPTION
    For a self-maintained fork: the published upstream image does not contain
    your changes, so this builds the image from source before starting.

    Re-running is safe: secrets already present in .env are never regenerated.
    Rotating TOTP_ENCRYPTION_KEY would invalidate every stored TOTP secret and
    every saved Prompt Audit endpoint token, so it is written exactly once.

.EXAMPLE
    .\one-click.ps1
    .\one-click.ps1 -LocalDirs
    .\one-click.ps1 -NoBuild
#>
[CmdletBinding()]
param(
    # Use host directories (docker-compose.local.yml) instead of named volumes.
    [switch]$LocalDirs,
    # Reuse the existing image / pull SUB2API_IMAGE instead of building.
    [switch]$NoBuild
)

$ErrorActionPreference = 'Stop'
Set-Location -Path $PSScriptRoot

$baseCompose = if ($LocalDirs) { 'docker-compose.local.yml' } else { 'docker-compose.yml' }
$doBuild = -not $NoBuild

# ---------------------------------------------------------------------------
# Prerequisites
# ---------------------------------------------------------------------------
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "docker is not installed or not on PATH. Install Docker Desktop first."
}
docker compose version *> $null
if ($LASTEXITCODE -ne 0) {
    throw "'docker compose' is unavailable. Update Docker Desktop."
}

# ---------------------------------------------------------------------------
# Secret helpers
# ---------------------------------------------------------------------------
function New-RandomHex {
    param([int]$Bytes = 32)
    $buffer = [byte[]]::new($Bytes)
    # Not RandomNumberGenerator.Fill: that API does not exist in .NET Framework,
    # so it throws on Windows PowerShell 5.1 before any secret is written.
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($buffer) } finally { $rng.Dispose() }
    -join ($buffer | ForEach-Object { $_.ToString('x2') })
}

# Runs a native command without letting its stderr become a terminating error:
# under $ErrorActionPreference = 'Stop', Windows PowerShell 5.1 turns redirected
# native stderr into exceptions.
function Invoke-NativeQuietly {
    param([scriptblock]$Command)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $Command 2>$null } finally { $ErrorActionPreference = $previous }
}

# Postgres reads POSTGRES_PASSWORD only when it first creates the database, and
# the backend reads ADMIN_PASSWORD only when it first creates the admin, so
# neither may be (re)generated for an existing stack: a new POSTGRES_PASSWORD
# locks sub2api out of its own database, and a new ADMIN_PASSWORD never applies.
function Test-PostgresInitialized {
    if ($LocalDirs) {
        return (Test-Path postgres_data) -and (@(Get-ChildItem -Force -LiteralPath postgres_data -ErrorAction SilentlyContinue).Count -gt 0)
    }
    $project = Invoke-NativeQuietly { docker compose -f $baseCompose config } |
        Select-String -Pattern '^name:\s*(\S+)' | Select-Object -First 1 |
        ForEach-Object { $_.Matches[0].Groups[1].Value }
    if (-not $project) { $project = (Split-Path -Leaf (Get-Location)).ToLowerInvariant() }
    $volumes = Invoke-NativeQuietly {
        docker volume ls -q --filter "label=com.docker.compose.project=$project" --filter 'label=com.docker.compose.volume=postgres_data'
    }
    return [bool]$volumes
}

# Values shipped in .env.example that must not survive into a real deployment.
$placeholders = @('', 'change_this_secure_password', 'your_password_here', 'changeme')

function Get-EnvValue {
    param([string]$Key)
    if (-not (Test-Path .env)) { return '' }
    # Last assignment wins, matching how docker compose reads the file.
    $line = Get-Content .env | Where-Object { $_ -match "^$([regex]::Escape($Key))=" } | Select-Object -Last 1
    if ($null -eq $line) { return '' }
    return $line.Substring($line.IndexOf('=') + 1)
}

$script:generated = @()

# Only fills a missing/placeholder value, so an existing secret survives a re-run.
function Set-EnvIfBlank {
    param([string]$Key, [string]$Value)
    $existing = Get-EnvValue -Key $Key
    if ($placeholders -notcontains $existing) { return }

    $content = Get-Content .env
    if ($content | Where-Object { $_ -match "^$([regex]::Escape($Key))=" }) {
        $content = $content | ForEach-Object {
            if ($_ -match "^$([regex]::Escape($Key))=") { "$Key=$Value" } else { $_ }
        }
        # UTF-8 without BOM: a BOM on the first line would corrupt that key name.
        [System.IO.File]::WriteAllLines((Resolve-Path .env), $content)
    } else {
        Add-Content -Path .env -Value "$Key=$Value"
    }
    $script:generated += $Key
}

# ---------------------------------------------------------------------------
# .env
# ---------------------------------------------------------------------------
if (-not (Test-Path .env)) {
    Copy-Item .env.example .env
    Write-Host "created .env from .env.example"
}

$databaseExists = Test-PostgresInitialized
if ($databaseExists -and ($placeholders -contains (Get-EnvValue -Key 'POSTGRES_PASSWORD'))) {
    Write-Warning ("the database already exists, so POSTGRES_PASSWORD is left as it is. " +
        "Postgres only applies it when creating the database; changing it here would lock sub2api out. " +
        "Rotate it inside Postgres first, then update .env.")
} else {
    Set-EnvIfBlank -Key 'POSTGRES_PASSWORD' -Value (New-RandomHex -Bytes 16)
}
Set-EnvIfBlank -Key 'JWT_SECRET'        -Value (New-RandomHex -Bytes 32)
# Must be 64 hex chars; the backend refuses to persist Prompt Audit endpoint
# tokens without a fixed key (they would not survive a restart).
Set-EnvIfBlank -Key 'TOTP_ENCRYPTION_KEY' -Value (New-RandomHex -Bytes 32)
if (-not $databaseExists) {
    Set-EnvIfBlank -Key 'ADMIN_PASSWORD' -Value (New-RandomHex -Bytes 12)
}

if ($doBuild -and ($placeholders -contains (Get-EnvValue -Key 'SUB2API_IMAGE'))) {
    Set-EnvIfBlank -Key 'SUB2API_IMAGE' -Value 'sub2api:local'
}

if ($script:generated.Count -gt 0) {
    Write-Host "filled in .env: $($script:generated -join ', ')"
}

if ($LocalDirs) {
    foreach ($dir in @('data', 'postgres_data', 'redis_data')) {
        if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir | Out-Null }
    }
}

# ---------------------------------------------------------------------------
# Build + start
# ---------------------------------------------------------------------------
$composeArgs = @('compose', '-f', $baseCompose)
$upArgs = @('up', '-d')
if ($doBuild) {
    $composeArgs += @('-f', 'docker-compose.build.yml')
    $upArgs += '--build'
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $commit = Invoke-NativeQuietly { git -C .. rev-parse --short HEAD }
        if ($LASTEXITCODE -eq 0 -and $commit) { $env:BUILD_COMMIT = $commit }
    }
    Write-Host "building image from source (this takes a few minutes on a cold cache)..."
}

& docker @composeArgs @upArgs
if ($LASTEXITCODE -ne 0) { throw "docker compose up failed with exit code $LASTEXITCODE" }

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------
$port = Get-EnvValue -Key 'SERVER_PORT'
if (-not $port) { $port = '8080' }

Write-Host ""
Write-Host "Sub2API is starting on http://localhost:$port"
Write-Host "  admin email:    $(Get-EnvValue -Key 'ADMIN_EMAIL')"
if ($script:generated -contains 'ADMIN_PASSWORD') {
    Write-Host "  admin password: $(Get-EnvValue -Key 'ADMIN_PASSWORD')"
} else {
    # Only the first boot applies ADMIN_PASSWORD; after that the admin changes it in the UI.
    Write-Host "  admin password: (unchanged - the one set when this stack was first created)"
}
Write-Host ""
Write-Host "Database migrations run automatically on first boot (AUTO_SETUP=true)."
Write-Host "Follow startup with:"
Write-Host "  docker $($composeArgs -join ' ') logs -f sub2api"
Write-Host ""
Write-Host "Prompt Audit (风控中心) is OFF by default. To enable it:"
Write-Host "  1. Admin > 系统设置 > turn on 风控总开关 (risk_control_enabled)"
Write-Host "  2. Admin > 提示词审计 > add an audit node, response contract = 自定义提示词"
Write-Host "  3. Use 试审 to check the prompt before enabling 同步阻止"

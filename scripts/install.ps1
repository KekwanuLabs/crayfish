#Requires -Version 5.1
<#
.SYNOPSIS
    Crayfish Windows Installer

.DESCRIPTION
    Installs Crayfish as a Windows background service (Task Scheduler).
    Downloads the binary, cloudflared (for phone calls), and piper TTS.
    Configures Windows Defender Firewall rules for LAN-only access.

.PARAMETER InstallDir
    Where to install Crayfish. Defaults to $env:LOCALAPPDATA\crayfish

.PARAMETER AutoStart
    Register Crayfish as a Task Scheduler task that starts at login.

.EXAMPLE
    # Run from PowerShell (may need to allow scripts):
    Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned
    iwr https://crayfish.sh/install.ps1 | iex
#>
param(
    [string]$InstallDir = "$env:LOCALAPPDATA\crayfish",
    [switch]$AutoStart = $true
)

$ErrorActionPreference = "Stop"

# ------------------------------------------------------------------ #
$Version = "latest"
$Repo    = "KekwanuLabs/crayfish"
$DataDir = $InstallDir
$BinDir  = "$DataDir\bin"
$Arch    = if ([System.Environment]::Is64BitOperatingSystem) { "amd64" } else { "amd64" }
# ------------------------------------------------------------------ #

function Write-Step($msg) { Write-Host "  -> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "  ✓  $msg" -ForegroundColor Green }
function Write-Warn($msg) { Write-Host "  ⚠  $msg" -ForegroundColor Yellow }

Write-Host ""
Write-Host "  ██████╗██████╗  █████╗ ██╗   ██╗███████╗██╗███████╗██╗  ██╗" -ForegroundColor DarkRed
Write-Host "  ██╔════╝██╔══██╗██╔══██╗╚██╗ ██╔╝██╔════╝██║██╔════╝██║  ██║" -ForegroundColor DarkRed
Write-Host "  ██║     ██████╔╝███████║ ╚████╔╝ █████╗  ██║███████╗███████║" -ForegroundColor DarkRed
Write-Host "  ██║     ██╔══██╗██╔══██║  ╚██╔╝  ██╔══╝  ██║╚════██║██╔══██║" -ForegroundColor DarkRed
Write-Host "  ╚██████╗██║  ██║██║  ██║   ██║   ██║     ██║███████║██║  ██║" -ForegroundColor DarkRed
Write-Host "   ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝   ╚═╝   ╚═╝     ╚═╝╚══════╝╚═╝  ╚═╝" -ForegroundColor DarkRed
Write-Host ""
Write-Host "  AI for the rest of us — Windows installer" -ForegroundColor White
Write-Host ""

# ------------------------------------------------------------------ #
# 1. Create directories
# ------------------------------------------------------------------ #
Write-Step "Creating directories at $DataDir"
$dirs = @($DataDir, $BinDir, "$DataDir\skills", "$DataDir\piper", "$DataDir\piper\models")
foreach ($d in $dirs) {
    if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
}
Write-Ok "Directories created"

# ------------------------------------------------------------------ #
# 2. Download Crayfish binary
# ------------------------------------------------------------------ #
$BinaryPath = "$BinDir\crayfish.exe"
if (-not (Test-Path $BinaryPath)) {
    Write-Step "Downloading Crayfish ($Arch)..."
    # Try GitHub API first; fall back to direct URL if rate-limited (60 req/hr unauthenticated).
    $Downloaded = $false
    $ApiUrl = "https://api.github.com/repos/$Repo/releases/latest"
    try {
        $response = Invoke-WebRequest -Uri $ApiUrl -UseBasicParsing -ErrorAction Stop
        if ($response.StatusCode -eq 200) {
            $release = $response.Content | ConvertFrom-Json
            $asset = $release.assets | Where-Object { $_.name -like "*windows-$Arch*" } | Select-Object -First 1
            if ($asset) {
                Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $BinaryPath -UseBasicParsing -ErrorAction Stop
                $Downloaded = $true
            } else {
                # Asset list is there but no Windows binary yet — use direct URL fallback.
                $tag = $release.tag_name
                $url = "https://github.com/$Repo/releases/download/$tag/crayfish-windows-$Arch.exe"
                Invoke-WebRequest -Uri $url -OutFile $BinaryPath -UseBasicParsing -ErrorAction Stop
                $Downloaded = $true
            }
        } elseif ($response.StatusCode -eq 403) {
            Write-Warn "GitHub API rate-limited — trying direct download..."
        }
    } catch {}

    if (-not $Downloaded) {
        # Direct fallback to the 'latest' release tag (no API call needed).
        $FallbackUrl = "https://github.com/$Repo/releases/download/latest/crayfish-windows-$Arch.exe"
        try {
            Invoke-WebRequest -Uri $FallbackUrl -OutFile $BinaryPath -UseBasicParsing -ErrorAction Stop
            $Downloaded = $true
        } catch {
            Write-Warn "Could not download Crayfish automatically."
            Write-Host "    Download manually from: https://github.com/$Repo/releases" -ForegroundColor Yellow
            Write-Host "    Place the .exe at: $BinaryPath" -ForegroundColor Yellow
        }
    }

    if ($Downloaded) {
        Write-Ok "Crayfish downloaded: $(([System.IO.FileInfo]$BinaryPath).Length / 1MB | [Math]::Round)MB"
    }
} else {
    Write-Ok "Crayfish binary already present"
}

# ------------------------------------------------------------------ #
# 3. Download cloudflared (for phone calls via Twilio)
# ------------------------------------------------------------------ #
$CloudflaredPath = "$BinDir\cloudflared.exe"
if (-not (Test-Path $CloudflaredPath)) {
    Write-Step "Downloading cloudflared (for phone calls)..."
    try {
        $url = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe"
        Invoke-WebRequest -Uri $url -OutFile $CloudflaredPath -UseBasicParsing
        Write-Ok "cloudflared downloaded"
    } catch {
        Write-Warn "cloudflared download failed (phone calls won't work until it's installed)"
    }
} else {
    Write-Ok "cloudflared already present"
}

# ------------------------------------------------------------------ #
# 4. Download piper TTS (for voice responses)
# ------------------------------------------------------------------ #
$PiperBin = "$DataDir\piper\piper.exe"
if (-not (Test-Path $PiperBin)) {
    Write-Step "Downloading Piper TTS..."
    try {
        $url = "https://github.com/rhasspy/piper/releases/latest/download/piper_windows_amd64.zip"
        $zipPath = "$env:TEMP\piper_windows.zip"
        Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing
        Expand-Archive -Path $zipPath -DestinationPath "$DataDir\piper" -Force
        Remove-Item $zipPath -Force -ErrorAction SilentlyContinue
        # Move files from piper subfolder if present
        $subdir = "$DataDir\piper\piper"
        if (Test-Path $subdir) {
            Get-ChildItem $subdir | Move-Item -Destination "$DataDir\piper" -Force
            Remove-Item $subdir -Force -ErrorAction SilentlyContinue
        }
        Write-Ok "Piper TTS installed"
    } catch {
        Write-Warn "Piper TTS download failed (voice synthesis will be unavailable)"
    }
}

# Download voice model (en_US-lessac-medium)
$ModelPath = "$DataDir\piper\models\en_US-lessac-medium.onnx"
if (-not (Test-Path $ModelPath)) {
    Write-Step "Downloading voice model (en_US-lessac-medium, ~65MB)..."
    try {
        $base = "https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/en/en_US/lessac/medium"
        Invoke-WebRequest -Uri "$base/en_US-lessac-medium.onnx"      -OutFile $ModelPath -UseBasicParsing
        Invoke-WebRequest -Uri "$base/en_US-lessac-medium.onnx.json" -OutFile "$ModelPath.json" -UseBasicParsing
        Write-Ok "Voice model installed"
    } catch {
        Write-Warn "Voice model download failed"
    }
}

# ------------------------------------------------------------------ #
# 5. Add to PATH
# ------------------------------------------------------------------ #
$currentPath = [System.Environment]::GetEnvironmentVariable("PATH", "User")
if ($currentPath -notlike "*$BinDir*") {
    Write-Step "Adding $BinDir to user PATH..."
    [System.Environment]::SetEnvironmentVariable("PATH", "$currentPath;$BinDir", "User")
    $env:PATH += ";$BinDir"
    Write-Ok "PATH updated (takes effect in new terminals)"
}

# ------------------------------------------------------------------ #
# 6. Windows Firewall — allow Crayfish dashboard from local network
# ------------------------------------------------------------------ #
Write-Step "Configuring Windows Firewall..."
$rules = @(
    @{ Name = "CrayfishDashboard"; Port = 8119; Description = "Crayfish dashboard (LAN only)" }
)
foreach ($rule in $rules) {
    # Remove existing rule if present
    netsh advfirewall firewall delete rule name=$($rule.Name) protocol=TCP dir=in 2>$null | Out-Null
    # Add new rule — allow from local subnet only
    netsh advfirewall firewall add rule `
        name=$($rule.Name) `
        description=$($rule.Description) `
        protocol=TCP `
        dir=in `
        localport=$($rule.Port) `
        action=allow `
        remoteip=LocalSubnet | Out-Null
}
Write-Ok "Firewall rules set (dashboard accessible from LAN only)"

# ------------------------------------------------------------------ #
# 7. Register as Task Scheduler task (auto-start at login)
# ------------------------------------------------------------------ #
if ($AutoStart -and (Test-Path $BinaryPath)) {
    Write-Step "Registering Crayfish as a startup task..."
    $taskName = "Crayfish"
    # Remove existing task
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue

    $action  = New-ScheduledTaskAction -Execute $BinaryPath -WorkingDirectory $DataDir
    $trigger = New-ScheduledTaskTrigger -AtLogon
    $settings = New-ScheduledTaskSettingsSet `
        -ExecutionTimeLimit (New-TimeSpan -Days 365) `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -StartWhenAvailable

    Register-ScheduledTask `
        -TaskName $taskName `
        -Action $action `
        -Trigger $trigger `
        -Settings $settings `
        -RunLevel Highest `
        -Force | Out-Null

    Write-Ok "Task Scheduler task registered — Crayfish starts at login"
}

# ------------------------------------------------------------------ #
# 8. Create default config directory
# ------------------------------------------------------------------ #
$ConfigDir = "$DataDir\config"
if (-not (Test-Path $ConfigDir)) {
    New-Item -ItemType Directory -Path $ConfigDir -Force | Out-Null
}

# ------------------------------------------------------------------ #
# Done
# ------------------------------------------------------------------ #
Write-Host ""
Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Green
Write-Host "  ✓  Crayfish installed!" -ForegroundColor Green
Write-Host "  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" -ForegroundColor Green
Write-Host ""
Write-Host "  Install directory : $DataDir" -ForegroundColor White
Write-Host "  Binary            : $BinaryPath" -ForegroundColor White
Write-Host ""

if (Test-Path $BinaryPath) {
    Write-Host "  Starting Crayfish..." -ForegroundColor Cyan
    Write-Host ""
    Write-Host "  Open your browser to: http://localhost:8119" -ForegroundColor Yellow
    Write-Host "  Complete the setup wizard to enter your AI API key." -ForegroundColor Gray
    Write-Host ""
    Start-Process -FilePath $BinaryPath -WorkingDirectory $DataDir -WindowStyle Hidden
} else {
    Write-Host "  ⚠  Binary not found. Download from:" -ForegroundColor Yellow
    Write-Host "     https://github.com/$Repo/releases" -ForegroundColor Cyan
    Write-Host "     Place crayfish-windows-amd64.exe at: $BinaryPath" -ForegroundColor Gray
}

Write-Host ""

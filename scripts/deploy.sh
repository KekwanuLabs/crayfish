#!/usr/bin/env bash
# Deploy Crayfish from MacBook to a remote device (Pi, server, etc.)
# Handles everything: build, first-time setup, binary push, service restart.
#
# Usage:
#   ./scripts/deploy.sh              # prompts for IP if not saved
#   ./scripts/deploy.sh 10.0.0.121   # use this IP
#   make deploy                      # calls this script
#
# Accessible AI for everyone.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${GREEN}[deploy]${NC} $*"; }
warn()  { echo -e "${YELLOW}[deploy]${NC} $*"; }
step()  { echo -e "${CYAN}==> ${NC}$*"; }
error() { echo -e "${RED}[deploy]${NC} $*" >&2; }

DEPLOY_CONF=".deploy.env"
BINARY="crayfish"
SERVICE="crayfish"
CLEAN_INSTALL=false

# Parse arguments
for arg in "$@"; do
    case "$arg" in
        --clean|-c)
            CLEAN_INSTALL=true
            shift
            ;;
    esac
done

# ==================================================================
# Load or prompt for deploy config
# ==================================================================

load_config() {
    if [ -n "${1:-}" ]; then
        PI_HOST="$1"
    elif [ -f "$DEPLOY_CONF" ]; then
        source "$DEPLOY_CONF"
        info "Loaded config from ${DEPLOY_CONF}"
    fi

    # Prompt for host if not set.
    if [ -z "${PI_HOST:-}" ]; then
        echo ""
        echo "  Where is your Crayfish device?"
        echo "  Enter the IP address or hostname of your Pi/server."
        echo ""
        read -rp "  IP address: " PI_HOST
        echo ""

        if [ -z "$PI_HOST" ]; then
            error "No IP address provided."
            exit 1
        fi
    fi

    # Prompt for SSH user if not set.
    if [ -z "${PI_USER:-}" ]; then
        read -rp "  SSH user on the device: " PI_USER
        if [ -z "$PI_USER" ]; then
            error "SSH user is required."
            exit 1
        fi
        echo ""
    fi

    # Prompt for architecture if not set.
    if [ -z "${PI_ARCH:-}" ]; then
        echo "  What architecture is the device?"
        echo "    1) armv7  — Raspberry Pi 2 (default)"
        echo "    2) arm64  — Raspberry Pi 3/4/5, Orange Pi"
        echo "    3) armv6  — Raspberry Pi 1, Pi Zero"
        echo "    4) amd64  — x86_64 server/NUC"
        echo ""
        read -rp "  Choice [1]: " arch_choice
        case "${arch_choice:-1}" in
            1) PI_ARCH="linux-armv7" ;;
            2) PI_ARCH="linux-arm64" ;;
            3) PI_ARCH="linux-armv6" ;;
            4) PI_ARCH="linux-amd64" ;;
            *) PI_ARCH="linux-armv7" ;;
        esac
        echo ""
    fi

    # Save for next time.
    cat > "$DEPLOY_CONF" << EOF
# Crayfish deploy config — auto-generated. Delete to reconfigure.
PI_HOST=${PI_HOST}
PI_USER=${PI_USER}
PI_ARCH=${PI_ARCH}
EOF
    info "Saved deploy config to ${DEPLOY_CONF} (delete to reconfigure)"
}

# ==================================================================
# Test SSH connectivity
# ==================================================================

test_ssh() {
    step "Testing SSH to ${PI_USER}@${PI_HOST}..."
    if ! ssh -o ConnectTimeout=5 -o BatchMode=yes "${PI_USER}@${PI_HOST}" 'echo ok' &>/dev/null; then
        # BatchMode failed, try without (might prompt for password).
        if ! ssh -o ConnectTimeout=5 "${PI_USER}@${PI_HOST}" 'echo ok' &>/dev/null; then
            error "Cannot SSH to ${PI_USER}@${PI_HOST}"
            echo ""
            echo "  Make sure:"
            echo "    1. The device is on and connected to your network"
            echo "    2. SSH is enabled (on Pi: sudo raspi-config > Interface Options > SSH)"
            echo "    3. You can reach it: ping ${PI_HOST}"
            echo ""
            echo "  Set up SSH keys for passwordless deploys:"
            echo "    ssh-copy-id ${PI_USER}@${PI_HOST}"
            echo ""
            exit 1
        fi
    fi
    info "SSH connection OK"
}

# ==================================================================
# Build
# ==================================================================

build_binary() {
    step "Building ${BINARY}-${PI_ARCH}..."

    # Extract GOOS and GOARCH from PI_ARCH.
    local goos goarch goarm=""
    goos=$(echo "$PI_ARCH" | cut -d- -f1)
    local arch_part=$(echo "$PI_ARCH" | cut -d- -f2)

    case "$arch_part" in
        armv6)  goarch="arm"; goarm="6" ;;
        armv7)  goarch="arm"; goarm="7" ;;
        arm64)  goarch="arm64"; goarm="" ;;
        amd64)  goarch="amd64"; goarm="" ;;
    esac

    local version commit build_time ldflags
    version=$(git describe --tags --abbrev=0 2>/dev/null || echo "0.4.0-dev")
    commit=$(git rev-parse --short HEAD 2>/dev/null || echo "dev")
    build_time=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
    ldflags="-s -w -X main.version=${version} -X main.commit=${commit} -X main.buildTime=${build_time}"

    # Inject Google OAuth credentials if available (from .env or environment).
    if [ -f .env ]; then
        set -a; source .env; set +a
    fi
    if [ -n "${GOOGLE_CLIENT_ID:-}" ]; then
        local oauth_pkg="github.com/KekwanuLabs/crayfish/internal/oauth"
        ldflags="${ldflags} -X '${oauth_pkg}.CrayfishClientID=${GOOGLE_CLIENT_ID}' -X '${oauth_pkg}.CrayfishClientSecret=${GOOGLE_CLIENT_SECRET}'"
    fi

    # Build with environment variables. GOARM is only set for ARM builds.
    export CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch"
    if [ -n "$goarm" ]; then
        export GOARM="$goarm"
    fi
    go build -trimpath -ldflags="$ldflags" -o "${BINARY}-${PI_ARCH}" ./cmd/crayfish/
    unset CGO_ENABLED GOOS GOARCH GOARM

    local size
    size=$(stat -f%z "${BINARY}-${PI_ARCH}" 2>/dev/null || stat -c%s "${BINARY}-${PI_ARCH}" 2>/dev/null)
    info "Built ${BINARY}-${PI_ARCH} ($(( size / 1024 / 1024 ))MB)"
}

# ==================================================================
# First-time setup (idempotent — safe to run multiple times)
# ==================================================================

setup_remote() {
    step "Setting up Crayfish on ${PI_HOST}..."

    # Detect the remote user's home directory and set up data/config there.
    ssh "${PI_USER}@${PI_HOST}" bash -s << 'SETUP_SCRIPT'
set -e

CRAYFISH_DATA="$HOME/.crayfish"
CRAYFISH_CONFIG_DIR="$HOME/.config/crayfish"

# Create data, bin, and config directories.
mkdir -p "$CRAYFISH_DATA/bin"
mkdir -p "$CRAYFISH_DATA/skills"
mkdir -p "$CRAYFISH_CONFIG_DIR"

# Create env file if missing (for API keys).
if [ ! -f "$CRAYFISH_CONFIG_DIR/env" ]; then
    echo '# Crayfish secrets — API keys go here.' > "$CRAYFISH_CONFIG_DIR/env"
    chmod 600 "$CRAYFISH_CONFIG_DIR/env"
    echo "[deploy] Created $CRAYFISH_CONFIG_DIR/env"
fi

echo "[deploy] Directories ready: $CRAYFISH_DATA, $CRAYFISH_CONFIG_DIR"
SETUP_SCRIPT

    info "Remote directories ready"

    # Install build dependencies for voice recognition (cmake, g++, git, ffmpeg)
    step "Installing voice recognition dependencies..."
    ssh "${PI_USER}@${PI_HOST}" bash -s << 'DEPS_SCRIPT'
# Check if we need to install deps
NEED_INSTALL=0
command -v cmake >/dev/null 2>&1 || NEED_INSTALL=1
command -v g++ >/dev/null 2>&1 || NEED_INSTALL=1
command -v git >/dev/null 2>&1 || NEED_INSTALL=1
command -v ffmpeg >/dev/null 2>&1 || NEED_INSTALL=1

if [ "$NEED_INSTALL" -eq 1 ]; then
    echo "[deploy] Installing build tools for voice recognition..."
    sudo apt-get update -qq
    sudo apt-get install -y -qq cmake g++ git ffmpeg >/dev/null 2>&1 || {
        echo "[deploy] Warning: Could not install some dependencies (voice may not work)"
    }
    echo "[deploy] Build tools installed"
else
    echo "[deploy] Build tools already installed"
fi
DEPS_SCRIPT

    info "Dependencies checked"
}

# ==================================================================
# Install systemd service
# ==================================================================

install_service() {
    step "Installing systemd service..."

    # Detect remote user's home dir, UID, and RAM.
    local remote_info
    remote_info=$(ssh "${PI_USER}@${PI_HOST}" 'echo "$HOME $(id -u) $(id -g) $(grep MemTotal /proc/meminfo | awk "{print int(\$2/1024)}")"')
    local remote_home remote_uid remote_gid ram_mb
    remote_home=$(echo "$remote_info" | awk '{print $1}')
    remote_uid=$(echo "$remote_info" | awk '{print $2}')
    remote_gid=$(echo "$remote_info" | awk '{print $3}')
    ram_mb=$(echo "$remote_info" | awk '{print $4}')

    # Memory limits: crayfish base ~30MB + piper TTS ~200MB peak + whisper ~100MB.
    # TTS (piper ONNX) is the largest consumer — needs headroom or synthesis stalls.
    local memory_max="512M" memory_high="400M"
    if [ "$ram_mb" -le 512 ] 2>/dev/null; then
        memory_max="128M"; memory_high="100M"   # Pi Zero/1 — no TTS
    elif [ "$ram_mb" -le 1024 ] 2>/dev/null; then
        memory_max="512M"; memory_high="400M"   # Pi 2 (921MB) — TTS needs room
    elif [ "$ram_mb" -le 4096 ] 2>/dev/null; then
        memory_max="768M"; memory_high="600M"   # Pi 3/4 (1-4GB)
    else
        memory_max="1G"; memory_high="800M"     # Pi 5 / desktop
    fi

    info "Remote: home=${remote_home}, RAM=${ram_mb}MB, limits=${memory_max}/${memory_high}"

    local crayfish_data="${remote_home}/.crayfish"
    local crayfish_config="${remote_home}/.config/crayfish"

    # Write systemd service file. Runs as the SSH user, stores data in their home.
    ssh "${PI_USER}@${PI_HOST}" "sudo tee /etc/systemd/system/crayfish.service > /dev/null" << EOF
[Unit]
Description=Crayfish — Agentic AI for the Rest of the World
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=${PI_USER}
Group=${PI_USER}

# Create directories before starting (handles fresh installs and resets)
ExecStartPre=/bin/mkdir -p ${crayfish_data}/bin
ExecStartPre=/bin/mkdir -p ${crayfish_data}/skills
ExecStartPre=/bin/mkdir -p ${crayfish_config}
ExecStartPre=/bin/sh -c 'test -f ${crayfish_config}/env || echo "# Crayfish secrets" > ${crayfish_config}/env'

ExecStart=${crayfish_data}/bin/crayfish
WorkingDirectory=${crayfish_data}
Restart=always
RestartSec=5

# Environment
EnvironmentFile=-${crayfish_config}/env
Environment=CRAYFISH_CONFIG=${crayfish_config}/crayfish.yaml
Environment=CRAYFISH_DB_PATH=${crayfish_data}/crayfish.db
Environment=CRAYFISH_LISTEN=:8119
Environment=CRAYFISH_AUTO_UPDATE=true

# Memory limits (tuned for ${ram_mb}MB RAM)
MemoryMax=${memory_max}
MemoryHigh=${memory_high}
CPUQuota=80%

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${crayfish_data} ${crayfish_config}
PrivateTmp=true

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=crayfish

[Install]
WantedBy=multi-user.target
EOF

    ssh "${PI_USER}@${PI_HOST}" 'sudo systemctl daemon-reload'
    info "Systemd service installed (runs as ${PI_USER})"
}

# ==================================================================
# Clean install (wipe all data)
# ==================================================================

clean_remote() {
    step "Wiping all Crayfish data on ${PI_HOST}..."
    warn "This will delete all settings, database, and downloaded models!"

    ssh "${PI_USER}@${PI_HOST}" bash -s << 'CLEAN_SCRIPT'
set -e
echo "[deploy] Stopping service..."
sudo systemctl stop crayfish 2>/dev/null || true

echo "[deploy] Removing data directories..."
rm -rf ~/.crayfish
rm -rf ~/.config/crayfish

echo "[deploy] Data wiped. Fresh start!"
CLEAN_SCRIPT

    info "Remote data wiped"
}

# ==================================================================
# Push binary and restart
# ==================================================================

push_and_restart() {
    step "Pushing binary to ${PI_HOST}..."
    scp "${BINARY}-${PI_ARCH}" "${PI_USER}@${PI_HOST}:/tmp/${BINARY}-new"

    step "Syncing skills..."
    scp skills/*.yaml "${PI_USER}@${PI_HOST}:~/.crayfish/skills/" 2>/dev/null || true

    step "Swapping binary and restarting..."
    ssh "${PI_USER}@${PI_HOST}" bash -s << 'RESTART_SCRIPT'
set -e
sudo systemctl stop crayfish 2>/dev/null || true
sleep 1

# Move binary into data dir (writable under ProtectSystem=strict)
mkdir -p "$HOME/.crayfish/bin"
mv /tmp/crayfish-new "$HOME/.crayfish/bin/crayfish"
chmod +x "$HOME/.crayfish/bin/crayfish"

# Clean up old location if it exists
sudo rm -f /usr/local/bin/crayfish 2>/dev/null || true

sudo systemctl enable --now crayfish
echo "[deploy] Service restarted"
RESTART_SCRIPT

    info "Binary deployed and service started"
}

# ==================================================================
# Health check
# ==================================================================

health_check() {
    step "Checking health..."
    sleep 3

    local status
    status=$(ssh "${PI_USER}@${PI_HOST}" 'curl -s -o /dev/null -w "%{http_code}" http://localhost:8119/health 2>/dev/null' || echo "000")

    if [ "$status" = "200" ]; then
        info "Health check passed!"
    else
        warn "Health check returned ${status} (might still be starting up)"
        warn "Check logs: ssh ${PI_USER}@${PI_HOST} 'journalctl -u crayfish -f'"
    fi
}

# ==================================================================
# Main
# ==================================================================

echo ""
echo "  Crayfish Deploy"
echo ""

load_config "${1:-}"
test_ssh
build_binary

# Wipe data if --clean flag was passed
if [ "$CLEAN_INSTALL" = true ]; then
    clean_remote
fi

setup_remote
install_service
push_and_restart
health_check

echo ""
echo "  ========================================="
echo "  Deployed to ${PI_HOST}!"
echo "  ========================================="
echo ""
echo "  Open http://${PI_HOST}:8119 in your browser"
echo "  to complete setup (if first time)."
echo ""
echo "  Logs:   ssh ${PI_USER}@${PI_HOST} 'journalctl -u crayfish -f'"
echo "  Status: ssh ${PI_USER}@${PI_HOST} 'sudo systemctl status crayfish'"
echo ""
echo "  Next deploy: just run 'make deploy' again."
echo ""

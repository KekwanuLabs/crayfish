#!/usr/bin/env bash
# Crayfish installer — works on anything from a Pi Zero to a Mac Studio.
# Usage: curl -sSL https://crayfish.sh/install | bash
#
# Supported platforms:
#   Linux:
#     - Raspberry Pi 1, Zero, Zero W          (ARMv6, 32-bit)
#     - Raspberry Pi 2                         (ARMv7, 32-bit)
#     - Raspberry Pi 3/4/5, Zero 2W           (ARM64 or ARMv7 depending on OS)
#     - Orange Pi, Banana Pi, Rock Pi, etc.   (ARM64)
#     - x86_64 servers, NUCs, old laptops     (amd64)
#   macOS:
#     - Mac Mini, MacBook, iMac (Intel)       (amd64)
#     - Mac Mini, MacBook, iMac (M1-M4)      (arm64)
#
# Accessible AI for everyone.

set -euo pipefail

CRAYFISH_VERSION="${CRAYFISH_VERSION:-0.4.0}"
CRAYFISH_BIN="/usr/local/bin/crayfish"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[crayfish]${NC} $*"; }
warn()  { echo -e "${YELLOW}[crayfish]${NC} $*"; }
error() { echo -e "${RED}[crayfish]${NC} $*" >&2; }

echo ""
echo "  Crayfish v${CRAYFISH_VERSION}"
echo "  Accessible AI for everyone."
echo ""

# ==================================================================
# Detect platform
# ==================================================================

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    linux)  OS="linux" ;;
    darwin) OS="darwin" ;;
    *)      error "Unsupported OS: $OS (Crayfish supports Linux and macOS)"; exit 1 ;;
esac

detect_arch() {
    local machine
    machine=$(uname -m)

    # On Linux ARM, check if 64-bit kernel is running 32-bit userland
    # (common on Pi 3/4/5 with 32-bit Raspberry Pi OS).
    local kernel_bits
    kernel_bits=$(getconf LONG_BIT 2>/dev/null || echo "unknown")

    case "$machine" in
        armv6l)         echo "armv6" ;;
        armv7l)         echo "armv7" ;;
        aarch64|arm64)  echo "arm64" ;;
        x86_64|amd64)   echo "amd64" ;;
        i386|i686)
            error "32-bit x86 is not supported. Crayfish needs a 64-bit or ARM system."
            exit 1
            ;;
        *)
            error "Unknown architecture: $machine"
            error "Please open an issue: https://github.com/KekwanuLabs/crayfish/issues"
            exit 1
            ;;
    esac
}

detect_board() {
    if [ "$OS" = "darwin" ]; then
        sysctl -n hw.model 2>/dev/null || echo "Mac"
    elif [ -f /proc/device-tree/model ]; then
        cat /proc/device-tree/model 2>/dev/null | tr -d '\0' || echo "Unknown"
    elif [ -f /sys/firmware/devicetree/base/model ]; then
        cat /sys/firmware/devicetree/base/model 2>/dev/null | tr -d '\0' || echo "Unknown"
    else
        echo "Generic Linux ($(uname -m))"
    fi
}

detect_ram_mb() {
    if [ "$OS" = "darwin" ]; then
        local mem_bytes
        mem_bytes=$(sysctl -n hw.memsize 2>/dev/null || echo "0")
        echo $((mem_bytes / 1024 / 1024))
    else
        local mem_kb
        mem_kb=$(grep MemTotal /proc/meminfo 2>/dev/null | awk '{print $2}')
        if [ -n "$mem_kb" ]; then
            echo $((mem_kb / 1024))
        else
            echo "0"
        fi
    fi
}

ARCH=$(detect_arch)
BOARD=$(detect_board)
RAM_MB=$(detect_ram_mb)

# macOS only supports amd64 and arm64.
if [ "$OS" = "darwin" ] && [ "$ARCH" != "amd64" ] && [ "$ARCH" != "arm64" ]; then
    error "macOS on $ARCH is not supported."
    exit 1
fi

info "Platform:     $OS"
info "Board:        $BOARD"
info "Architecture: $ARCH"
info "RAM:          ${RAM_MB}MB"
echo ""

# ==================================================================
# Download and verify binary
# ==================================================================

BINARY="crayfish-${OS}-${ARCH}"
DOWNLOAD_URL="https://github.com/KekwanuLabs/crayfish/releases/download/v${CRAYFISH_VERSION}/${BINARY}"
info "Downloading ${BINARY}..."

if command -v curl &>/dev/null; then
    curl -sSL "${DOWNLOAD_URL}" -o /tmp/crayfish
elif command -v wget &>/dev/null; then
    wget -q "${DOWNLOAD_URL}" -O /tmp/crayfish
else
    error "Neither curl nor wget found."
    exit 1
fi

chmod +x /tmp/crayfish

# Verify the binary is valid for this architecture.
if ! /tmp/crayfish --version &>/dev/null 2>&1; then
    if command -v file &>/dev/null; then
        if ! file /tmp/crayfish | grep -q "executable"; then
            error "Downloaded binary doesn't appear valid for this architecture."
            error "Expected: $ARCH | Got: $(file /tmp/crayfish)"
            exit 1
        fi
    fi
fi

info "Binary verified"

# Install binary.
if [ -w "$(dirname $CRAYFISH_BIN)" ]; then
    mv /tmp/crayfish "${CRAYFISH_BIN}"
else
    sudo mv /tmp/crayfish "${CRAYFISH_BIN}"
fi
info "Installed to ${CRAYFISH_BIN}"

# ==================================================================
# Platform-specific service setup
# ==================================================================

if [ "$OS" = "linux" ]; then
    # ---- Linux: systemd ----
    CRAYFISH_USER="crayfish"
    CRAYFISH_HOME="/var/lib/crayfish"
    CRAYFISH_CONFIG_DIR="/etc/crayfish"

    # System user.
    if ! id "${CRAYFISH_USER}" &>/dev/null; then
        sudo useradd -r -s /sbin/nologin -d "${CRAYFISH_HOME}" "${CRAYFISH_USER}"
        info "Created system user: ${CRAYFISH_USER}"
    fi

    # Directories.
    sudo mkdir -p "${CRAYFISH_HOME}" "${CRAYFISH_CONFIG_DIR}" "${CRAYFISH_HOME}/skills"
    sudo chown -R "${CRAYFISH_USER}:${CRAYFISH_USER}" "${CRAYFISH_HOME}"
    sudo chown -R "${CRAYFISH_USER}:${CRAYFISH_USER}" "${CRAYFISH_CONFIG_DIR}"

    # Config file.
    if [ ! -f "${CRAYFISH_CONFIG_DIR}/crayfish.yaml" ]; then
        cat << 'YAMLEOF' | sudo tee "${CRAYFISH_CONFIG_DIR}/crayfish.yaml" > /dev/null
# Crayfish Configuration
# Env vars override everything here.

db_path: "/var/lib/crayfish/crayfish.db"
listen_addr: ":8119"
auto_update: true

# LLM Provider: anthropic, openai, groq, deepseek, ollama, vllm, lmstudio, together, openrouter
provider: ""
model: ""
max_tokens: 1024

# For local LLM (Ollama), uncomment:
# provider: "ollama"
# model: "tinyllama"
# endpoint: "http://localhost:11434/v1/chat/completions"

# Telegram bot token (get from @BotFather)
telegram_token: ""
YAMLEOF
        sudo chmod 640 "${CRAYFISH_CONFIG_DIR}/crayfish.yaml"
        sudo chown "${CRAYFISH_USER}:${CRAYFISH_USER}" "${CRAYFISH_CONFIG_DIR}/crayfish.yaml"
        info "Created config: ${CRAYFISH_CONFIG_DIR}/crayfish.yaml"
    fi

    # Secrets file.
    if [ ! -f "${CRAYFISH_CONFIG_DIR}/env" ]; then
        cat << 'ENVEOF' | sudo tee "${CRAYFISH_CONFIG_DIR}/env" > /dev/null
# Crayfish secrets — API keys go here (not in crayfish.yaml).
#
# For cloud LLM (pick one):
# CRAYFISH_API_KEY=sk-ant-xxxxx          # Anthropic
# CRAYFISH_API_KEY=sk-xxxxx               # OpenAI
# CRAYFISH_API_KEY=gsk_xxxxx              # Groq
#
# For local LLM (Ollama), no key needed:
# CRAYFISH_PROVIDER=ollama
# CRAYFISH_MODEL=tinyllama
# CRAYFISH_ENDPOINT=http://localhost:11434/v1/chat/completions
ENVEOF
        sudo chmod 600 "${CRAYFISH_CONFIG_DIR}/env"
        sudo chown "${CRAYFISH_USER}:${CRAYFISH_USER}" "${CRAYFISH_CONFIG_DIR}/env"
        info "Created secrets: ${CRAYFISH_CONFIG_DIR}/env"
    fi

    # Tune memory limits based on RAM.
    MEMORY_MAX="128M"
    MEMORY_HIGH="100M"
    if [ "$RAM_MB" -gt 0 ]; then
        if [ "$RAM_MB" -le 512 ]; then
            MEMORY_MAX="96M"; MEMORY_HIGH="80M"
            warn "Low RAM detected (${RAM_MB}MB). Tightening memory limits."
        elif [ "$RAM_MB" -ge 4096 ]; then
            MEMORY_MAX="192M"; MEMORY_HIGH="160M"
        fi
    fi

    # Systemd unit.
    cat << EOF | sudo tee /etc/systemd/system/crayfish.service > /dev/null
[Unit]
Description=Crayfish — Agentic AI for the Rest of the World
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=${CRAYFISH_USER}
Group=${CRAYFISH_USER}
ExecStart=${CRAYFISH_BIN}
WorkingDirectory=${CRAYFISH_HOME}
Restart=always
RestartSec=5

# Environment
EnvironmentFile=-${CRAYFISH_CONFIG_DIR}/env
Environment=CRAYFISH_CONFIG=${CRAYFISH_CONFIG_DIR}/crayfish.yaml
Environment=CRAYFISH_AUTO_UPDATE=true

# Memory limits (tuned for ${RAM_MB}MB RAM)
MemoryMax=${MEMORY_MAX}
MemoryHigh=${MEMORY_HIGH}
CPUQuota=80%

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${CRAYFISH_HOME} ${CRAYFISH_CONFIG_DIR}
PrivateTmp=true

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=crayfish

[Install]
WantedBy=multi-user.target
EOF

    sudo systemctl daemon-reload
    info "Systemd service installed"

    CONFIG_PATH="${CRAYFISH_CONFIG_DIR}/env"
    START_CMD="sudo systemctl enable --now crayfish"
    LOG_CMD="journalctl -u crayfish -f"

    # Get the device's local IP for setup wizard URL.
    LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "your-device-ip")

elif [ "$OS" = "darwin" ]; then
    # ---- macOS: launchd ----
    CRAYFISH_HOME="$HOME/.crayfish"
    CRAYFISH_CONFIG_DIR="$CRAYFISH_HOME"
    PLIST_PATH="$HOME/Library/LaunchAgents/com.crayfish.agent.plist"

    mkdir -p "${CRAYFISH_HOME}" "${CRAYFISH_HOME}/skills" "$(dirname $PLIST_PATH)"

    # Config.
    if [ ! -f "${CRAYFISH_HOME}/crayfish.yaml" ]; then
        cat << 'YAMLEOF' > "${CRAYFISH_HOME}/crayfish.yaml"
# Crayfish Configuration
db_path: "~/.crayfish/crayfish.db"
listen_addr: ":8119"
auto_update: true
provider: ""
model: ""
max_tokens: 1024
telegram_token: ""
YAMLEOF
        info "Created ${CRAYFISH_HOME}/crayfish.yaml"
    fi

    # Env file for secrets.
    if [ ! -f "${CRAYFISH_HOME}/env" ]; then
        cat << 'ENVEOF' > "${CRAYFISH_HOME}/env"
# Crayfish secrets — API keys here.
# CRAYFISH_API_KEY=your-key
ENVEOF
        chmod 600 "${CRAYFISH_HOME}/env"
        info "Created ${CRAYFISH_HOME}/env"
    fi

    # launchd plist.
    cat << EOF > "${PLIST_PATH}"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.crayfish.agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>${CRAYFISH_BIN}</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>CRAYFISH_CONFIG</key>
        <string>${CRAYFISH_HOME}/crayfish.yaml</string>
        <key>CRAYFISH_DB_PATH</key>
        <string>${CRAYFISH_HOME}/crayfish.db</string>
    </dict>
    <key>WorkingDirectory</key>
    <string>${CRAYFISH_HOME}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${CRAYFISH_HOME}/crayfish.log</string>
    <key>StandardErrorPath</key>
    <string>${CRAYFISH_HOME}/crayfish.err</string>
</dict>
</plist>
EOF
    info "launchd plist installed"

    CONFIG_PATH="${CRAYFISH_HOME}/env"
    START_CMD="launchctl load ${PLIST_PATH}"
    LOG_CMD="tail -f ${CRAYFISH_HOME}/crayfish.log"
    LOCAL_IP="localhost"
fi

# ==================================================================
# Done!
# ==================================================================
echo ""
echo "  ========================================="
echo "  Crayfish installed successfully!"
echo "  ========================================="
echo ""
echo "  Platform: $OS/$ARCH ($BOARD)"
echo "  Binary:   ${CRAYFISH_BIN}"
echo "  Config:   ${CRAYFISH_CONFIG_DIR}/crayfish.yaml"
echo "  RAM:      ${RAM_MB}MB"
echo ""
echo "  STEP 1:  ${START_CMD}"
echo ""
echo "  STEP 2:  Open http://${LOCAL_IP}:8119 in your browser."
echo "           The setup wizard will walk you through the rest."
echo ""
echo "  Logs:    ${LOG_CMD}"
echo ""

if [ "$RAM_MB" -gt 0 ] && [ "$RAM_MB" -le 1024 ]; then
    warn "Low RAM (${RAM_MB}MB) — use a cloud LLM provider (Anthropic, OpenAI, Groq)."
    warn "For local LLM, run Ollama on another machine and point Crayfish to it."
    echo ""
fi

echo "  Accessible AI for everyone."
echo ""

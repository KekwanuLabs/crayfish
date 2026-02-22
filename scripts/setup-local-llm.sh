#!/usr/bin/env bash
# Crayfish Local LLM Setup — run AI without any cloud API keys.
# Installs Ollama and pulls a model sized for your hardware.
#
# Usage: ./setup-local-llm.sh
#        ./setup-local-llm.sh --model phi-2
#        ./setup-local-llm.sh --remote 192.168.1.50
#
# Accessible AI for everyone.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${GREEN}[crayfish]${NC} $*"; }
warn()  { echo -e "${YELLOW}[crayfish]${NC} $*"; }
error() { echo -e "${RED}[crayfish]${NC} $*" >&2; }

CRAYFISH_CONFIG_DIR="${CRAYFISH_CONFIG_DIR:-/etc/crayfish}"
REQUESTED_MODEL=""
REMOTE_HOST=""

# ---- Parse args ----
while [[ $# -gt 0 ]]; do
    case $1 in
        --model)   REQUESTED_MODEL="$2"; shift 2 ;;
        --remote)  REMOTE_HOST="$2"; shift 2 ;;
        --help|-h)
            echo "Usage: setup-local-llm.sh [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --model MODEL    Specify model to pull (e.g., tinyllama, phi-2, llama3)"
            echo "  --remote HOST    Point Crayfish at a remote Ollama instance"
            echo "  --help           Show this help"
            echo ""
            echo "Model recommendations by RAM:"
            echo "  4GB:   tinyllama (1.1B)  — fast, basic tasks"
            echo "  4GB:   phi-2 (2.7B)      — smarter, slower"
            echo "  8GB:   llama3 (8B)       — good all-rounder"
            echo "  8GB:   mistral (7B)      — strong reasoning"
            echo "  16GB:  llama3:70b-q4     — near-cloud quality"
            echo ""
            echo "For boards with <4GB RAM, use --remote to point at another machine."
            exit 0
            ;;
        *) error "Unknown option: $1"; exit 1 ;;
    esac
done

echo ""
echo "  Crayfish Local LLM Setup"
echo ""

# ---- Detect RAM ----
detect_ram_mb() {
    local mem_kb
    mem_kb=$(grep MemTotal /proc/meminfo 2>/dev/null | awk '{print $2}')
    if [ -n "$mem_kb" ]; then
        echo $((mem_kb / 1024))
    else
        echo "0"
    fi
}

RAM_MB=$(detect_ram_mb)
info "Detected RAM: ${RAM_MB}MB"

# ---- Remote mode ----
if [ -n "$REMOTE_HOST" ]; then
    info "Configuring Crayfish to use remote Ollama at ${REMOTE_HOST}..."

    ENDPOINT="http://${REMOTE_HOST}:11434/v1/chat/completions"
    MODEL="${REQUESTED_MODEL:-tinyllama}"

    # Test connection
    info "Testing connection to ${REMOTE_HOST}:11434..."
    if curl -sf "http://${REMOTE_HOST}:11434/api/tags" > /dev/null 2>&1; then
        info "Connection successful!"

        # List available models on remote
        echo ""
        info "Models available on remote:"
        curl -sf "http://${REMOTE_HOST}:11434/api/tags" 2>/dev/null | \
            python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for m in data.get('models', []):
        name = m.get('name', '?')
        size_gb = m.get('size', 0) / (1024**3)
        print(f'    - {name} ({size_gb:.1f}GB)')
except: pass
" 2>/dev/null || echo "    (could not list models)"
        echo ""
    else
        warn "Could not connect to ${REMOTE_HOST}:11434"
        warn "Make sure Ollama is running on that machine with:"
        warn "  OLLAMA_HOST=0.0.0.0 ollama serve"
    fi

    # Write config
    if [ -f "${CRAYFISH_CONFIG_DIR}/env" ]; then
        # Append to env file
        {
            echo ""
            echo "# Local LLM via remote Ollama (added by setup-local-llm.sh)"
            echo "CRAYFISH_PROVIDER=ollama"
            echo "CRAYFISH_MODEL=${MODEL}"
            echo "CRAYFISH_ENDPOINT=${ENDPOINT}"
            echo "CRAYFISH_API_KEY=not-needed"
        } | sudo tee -a "${CRAYFISH_CONFIG_DIR}/env" > /dev/null
        info "Updated ${CRAYFISH_CONFIG_DIR}/env"
    else
        echo "CRAYFISH_PROVIDER=ollama"    > /tmp/crayfish-env
        echo "CRAYFISH_MODEL=${MODEL}"    >> /tmp/crayfish-env
        echo "CRAYFISH_ENDPOINT=${ENDPOINT}" >> /tmp/crayfish-env
        echo "CRAYFISH_API_KEY=not-needed" >> /tmp/crayfish-env
        info "Environment config written. Set these env vars or copy to your config."
        cat /tmp/crayfish-env
    fi

    echo ""
    info "Done! Restart Crayfish: sudo systemctl restart crayfish"
    echo ""
    exit 0
fi

# ---- Local mode: check RAM ----
if [ "$RAM_MB" -lt 3500 ]; then
    echo ""
    error "Your board has ${RAM_MB}MB RAM."
    error "Local LLM inference needs at least 4GB RAM."
    echo ""
    echo "  Options:"
    echo ""
    echo "  1. Run Ollama on a more powerful machine and point Crayfish at it:"
    echo "     ./setup-local-llm.sh --remote 192.168.1.50"
    echo ""
    echo "  2. Use a cloud LLM provider (Anthropic, Groq, etc.):"
    echo "     Add CRAYFISH_API_KEY=your-key to ${CRAYFISH_CONFIG_DIR}/env"
    echo ""
    echo "  3. Use Groq (free tier, very fast, runs in cloud):"
    echo "     CRAYFISH_PROVIDER=groq CRAYFISH_API_KEY=gsk_xxx CRAYFISH_MODEL=llama3-8b-8192"
    echo ""
    exit 1
fi

# ---- Pick model based on RAM ----
if [ -n "$REQUESTED_MODEL" ]; then
    MODEL="$REQUESTED_MODEL"
elif [ "$RAM_MB" -ge 16000 ]; then
    MODEL="llama3"
    info "16GB+ RAM detected — recommending llama3 (8B)"
elif [ "$RAM_MB" -ge 8000 ]; then
    MODEL="mistral"
    info "8GB RAM detected — recommending mistral (7B)"
elif [ "$RAM_MB" -ge 4000 ]; then
    MODEL="tinyllama"
    info "4GB RAM detected — recommending tinyllama (1.1B)"
else
    MODEL="tinyllama"
fi

# ---- Install Ollama if not present ----
if command -v ollama &>/dev/null; then
    info "Ollama already installed: $(ollama --version 2>/dev/null || echo 'unknown version')"
else
    info "Installing Ollama..."
    curl -fsSL https://ollama.com/install.sh | sh

    if ! command -v ollama &>/dev/null; then
        error "Ollama installation failed. Try installing manually:"
        error "  curl -fsSL https://ollama.com/install.sh | sh"
        exit 1
    fi
    info "Ollama installed successfully"
fi

# ---- Ensure Ollama is running ----
if ! curl -sf http://localhost:11434/api/tags > /dev/null 2>&1; then
    info "Starting Ollama service..."
    sudo systemctl enable --now ollama 2>/dev/null || ollama serve &
    sleep 3

    if ! curl -sf http://localhost:11434/api/tags > /dev/null 2>&1; then
        warn "Ollama doesn't seem to be responding yet. It may need a moment."
    fi
fi

# ---- Pull model ----
info "Pulling model: ${MODEL}"
info "This may take a while depending on your connection..."
echo ""
ollama pull "$MODEL"
echo ""
info "Model ${MODEL} ready!"

# ---- Configure Crayfish ----
ENDPOINT="http://localhost:11434/v1/chat/completions"

if [ -f "${CRAYFISH_CONFIG_DIR}/env" ]; then
    # Check if already configured for ollama
    if grep -q "CRAYFISH_PROVIDER=ollama" "${CRAYFISH_CONFIG_DIR}/env" 2>/dev/null; then
        info "Crayfish already configured for Ollama — updating model to ${MODEL}"
        sudo sed -i "s/CRAYFISH_MODEL=.*/CRAYFISH_MODEL=${MODEL}/" "${CRAYFISH_CONFIG_DIR}/env"
    else
        {
            echo ""
            echo "# Local LLM via Ollama (added by setup-local-llm.sh)"
            echo "CRAYFISH_PROVIDER=ollama"
            echo "CRAYFISH_MODEL=${MODEL}"
            echo "CRAYFISH_ENDPOINT=${ENDPOINT}"
            echo "CRAYFISH_API_KEY=not-needed"
        } | sudo tee -a "${CRAYFISH_CONFIG_DIR}/env" > /dev/null
    fi
    info "Updated ${CRAYFISH_CONFIG_DIR}/env"
else
    info "Set these environment variables:"
    echo "  CRAYFISH_PROVIDER=ollama"
    echo "  CRAYFISH_MODEL=${MODEL}"
    echo "  CRAYFISH_ENDPOINT=${ENDPOINT}"
    echo "  CRAYFISH_API_KEY=not-needed"
fi

# ---- Make Ollama listen on all interfaces (for remote use) ----
echo ""
echo -e "  ${CYAN}Tip:${NC} To let OTHER Crayfish devices use this machine as their LLM server:"
echo "  sudo systemctl edit ollama"
echo "  Add:  Environment=\"OLLAMA_HOST=0.0.0.0\""
echo "  Then: sudo systemctl restart ollama"
echo ""

# ---- Done ----
echo "  ========================================="
echo "  Local LLM ready!"
echo "  ========================================="
echo ""
echo "  Provider: Ollama"
echo "  Model:    ${MODEL}"
echo "  Endpoint: ${ENDPOINT}"
echo ""
echo "  Restart Crayfish:"
echo "    sudo systemctl restart crayfish"
echo ""
echo "  Test it:"
echo "    CRAYFISH_PROVIDER=ollama CRAYFISH_MODEL=${MODEL} \\"
echo "    CRAYFISH_ENDPOINT=${ENDPOINT} CRAYFISH_API_KEY=x \\"
echo "    crayfish"
echo ""
echo "  Accessible AI for everyone."
echo ""

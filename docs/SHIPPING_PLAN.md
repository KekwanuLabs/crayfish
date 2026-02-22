# Crayfish Shipping Plan

**Version:** 0.4.0
**Target:** Ship today as open source
**Status:** Ready for release

---

## Executive Summary

Crayfish is ready to ship. All core components are implemented and wired up:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         CRAYFISH v0.4.0 - SHIP TODAY                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ✓ Telegram Gateway          ✓ Gmail Integration       ✓ Skills System     │
│  ✓ Web Setup Wizard          ✓ Security Guardrails     ✓ Named Identity    │
│  ✓ Voice (Piper TTS)         ✓ Trust/Pairing System    ✓ Memory System     │
│  ✓ On-Device LLM (Ollama)    ✓ MCP Client              ✓ Auto-Updater      │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 1. What Ships Today

### Core Features

| Feature | Status | Description |
|---------|--------|-------------|
| **Multi-Provider LLM** | ✅ Ready | Anthropic, OpenAI, Grok, Ollama, and more |
| **Telegram Bot** | ✅ Ready | Chat from anywhere via Telegram |
| **Gmail Integration** | ✅ Ready | Read, search, send emails |
| **Skills System** | ✅ Ready | Teachable behaviors via YAML or Web UI |
| **Memory System** | ✅ Ready | Automatic memory extraction and retrieval |
| **Web Setup Wizard** | ✅ Ready | Browser-based configuration |
| **Trust Tiers** | ✅ Ready | Operator → Trusted → Group → Unknown |
| **Pairing Codes** | ✅ Ready | 6-digit OTP for adding trusted users |
| **Security Guardrails** | ✅ Ready | Prompt injection detection, output sanitization |
| **Voice (TTS)** | ✅ Ready | Local Piper neural TTS |
| **On-Device LLM** | ✅ Ready | Ollama integration for offline operation |
| **MCP Support** | ✅ Ready | Connect external tools via MCP |
| **Auto-Update** | ✅ Ready | Self-updating from GitHub releases |

### Supported Platforms

| Platform | Architecture | Notes |
|----------|--------------|-------|
| Raspberry Pi 4/5 | arm64 | Primary target |
| Raspberry Pi Zero 2 W | arm64 | Works, slower |
| Linux x86_64 | amd64 | Development, servers |
| macOS | arm64/amd64 | Development |
| Windows (WSL) | amd64 | Development |

### LLM Providers

| Provider | Endpoint | Notes |
|----------|----------|-------|
| Anthropic (Claude) | Cloud | Recommended, best quality |
| OpenAI (GPT-4) | Cloud | Alternative |
| Grok (xAI) | Cloud | Alternative |
| **Ollama** | **Local** | **On-device, offline capable** |
| DeepSeek | Cloud | Budget option |
| Together AI | Cloud | Open models |
| OpenRouter | Cloud | Model aggregator |
| vLLM | Local | Self-hosted |
| LM Studio | Local | Desktop app |

---

## 2. On-Device LLM (Ollama)

For fully offline operation on Raspberry Pi, Crayfish supports Ollama:

### Recommended Models for Raspberry Pi

| Model | RAM | Speed | Quality | Use Case |
|-------|-----|-------|---------|----------|
| **Phi-3-mini (3.8B)** | 4GB | Fast | Good | Daily use, 4GB Pi |
| **Gemma 2B** | 2GB | Very Fast | Decent | Pi Zero 2 W |
| **TinyLlama (1.1B)** | 1GB | Very Fast | Basic | Minimal resources |
| **Llama 3.2 (3B)** | 4GB | Fast | Good | 8GB Pi recommended |
| **Mistral (7B)** | 8GB | Slower | Great | 8GB Pi only |

### Setup

```bash
# Install Ollama on Raspberry Pi
curl -fsSL https://ollama.com/install.sh | sh

# Pull a model (choose based on your Pi's RAM)
ollama pull phi3              # 4GB RAM
ollama pull gemma:2b          # 2GB RAM
ollama pull tinyllama         # 1GB RAM

# Start Ollama service
systemctl enable ollama
systemctl start ollama
```

### Configuration

```yaml
# crayfish.yaml
provider: ollama
model: phi3
endpoint: http://localhost:11434/v1/chat/completions
# No API key needed for local Ollama
```

Or via environment:

```bash
export CRAYFISH_PROVIDER=ollama
export CRAYFISH_MODEL=phi3
export CRAYFISH_ENDPOINT=http://localhost:11434/v1/chat/completions
```

### Performance Notes

- **Phi-3-mini on Pi 4 (4GB):** ~2-5 tokens/second
- **Gemma 2B on Pi Zero 2 W:** ~0.5-1 tokens/second
- **First response may be slow** (model loading)
- **Subsequent responses faster** (model cached in memory)

---

## 3. Architecture

### Component Wiring

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              CRAYFISH RUNTIME                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   main.go                                                                   │
│      │                                                                      │
│      ▼                                                                      │
│   ┌─────────┐                                                               │
│   │   App   │ ─────────────────────────────────────────────────────┐        │
│   └────┬────┘                                                      │        │
│        │                                                           │        │
│        ├───► Storage (SQLite) ◄──────────────────────────┐         │        │
│        │         │                                       │         │        │
│        ├───► Bus (Event System) ◄────────────────────────┤         │        │
│        │         │                                       │         │        │
│        ├───► SessionStore ◄──────────────────────────────┤         │        │
│        │         │                                       │         │        │
│        ├───► Provider (LLM) ───► Anthropic/Ollama/etc    │         │        │
│        │         │                                       │         │        │
│        ├───► ToolRegistry ◄──────────────────────────────┤         │        │
│        │         │                                       │         │        │
│        ├───► MemoryExtractor ◄───────────────────────────┤         │        │
│        │         │                                       │         │        │
│        ├───► MemoryRetriever ◄───────────────────────────┤         │        │
│        │         │                                       │         │        │
│        ├───► Runtime ◄──────────────────────────┐        │         │        │
│        │         │                              │        │         │        │
│        │         ├── Guardrails                 │        │         │        │
│        │         ├── PairingService             │        │         │        │
│        │         └── OfflineQueue               │        │         │        │
│        │                                        │        │         │        │
│        └───► Gateway ───────────────────────────┴────────┴─────────┘        │
│                  │                                                          │
│                  ├── HTTP Server (:8119)                                    │
│                  ├── Skills API (/api/skills/*)                             │
│                  ├── Skills UI (/skills)                                    │
│                  └── Channel Adapters                                       │
│                       ├── Telegram                                          │
│                       └── CLI                                               │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Data Flow

```
User Message (Telegram/CLI)
         │
         ▼
   ┌───────────┐
   │  Gateway  │ ── Validates, routes to adapter
   └─────┬─────┘
         │
         ▼
   ┌───────────┐
   │    Bus    │ ── Publishes message.inbound event
   └─────┬─────┘
         │
         ▼
   ┌───────────┐
   │  Runtime  │ ── Processes message
   └─────┬─────┘
         │
         ├── Security check (Guardrails)
         ├── Pairing code check
         ├── Trust tier validation
         ├── Memory retrieval (context)
         ├── LLM call (with tools)
         ├── Memory extraction (from response)
         │
         ▼
   ┌───────────┐
   │    Bus    │ ── Publishes message.outbound event
   └─────┬─────┘
         │
         ▼
   ┌───────────┐
   │  Gateway  │ ── Routes to channel adapter
   └─────┬─────┘
         │
         ▼
   ┌───────────┐
   │ Telegram  │ ── Sends response to user
   └───────────┘
```

---

## 4. Database Schema

### Core Tables

| Table | Purpose |
|-------|---------|
| `events` | Event bus log |
| `sessions` | User sessions with trust tiers |
| `messages` | Conversation history |
| `memory_fts` | Full-text searchable memories |
| `memory_metadata` | Memory importance, categories |
| `emails` | Gmail email metadata |
| `emails_fts` | Email full-text search |
| `todos` | Built-in todo list |
| `pairing_otps` | Pairing codes |
| `offline_queue` | Message queue for offline mode |

### Future-Proofing (Fabric Protocol)

The following columns are reserved for future Fabric Protocol integration:

```sql
-- Added in migration 5
ALTER TABLE sessions ADD COLUMN fabric_agent_id TEXT;
ALTER TABLE sessions ADD COLUMN fabric_delegation BLOB;
```

These will enable cryptographic identity binding when Fabric Protocol ships.

---

## 5. Security Model

### Trust Tiers

| Tier | Value | Capabilities |
|------|-------|--------------|
| Operator | 3 | Full access, can generate pairing codes |
| Trusted | 2 | All tools, read/send emails |
| Group | 1 | Basic chat only |
| Unknown | 0 | Must provide pairing code |

### Guardrails

- **Prompt Injection Detection:** Patterns for override attempts
- **Output Sanitization:** Redact sensitive data
- **Skill Validation:** No shell commands, no external URLs
- **Rate Limiting:** Pairing attempts limited

---

## 6. Deployment Options

### Option A: Automated Install (Recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.sh | bash
```

### Option B: Manual Build

```bash
git clone https://github.com/KekwanuLabs/crayfish.git
cd crayfish
go build -o crayfish ./cmd/crayfish
./crayfish
```

### Option C: Docker

```bash
docker run -d \
  -v ~/.config/crayfish:/root/.config/crayfish \
  -p 8119:8119 \
  ghcr.io/KekwanuLabs/crayfish:latest
```

---

## 7. Release Checklist

### Pre-Release

- [x] All components wired up in app.go
- [x] Database migrations complete
- [x] On-device LLM (Ollama) working
- [x] Telegram integration tested
- [x] Gmail integration tested
- [x] Skills system tested
- [x] Web UI tested
- [x] Security guardrails in place
- [ ] Build passes on all platforms
- [ ] README complete
- [ ] SECURITY.md complete

### Release Steps

1. Tag version: `git tag v0.4.0`
2. Push tag: `git push origin v0.4.0`
3. GitHub Actions builds binaries
4. Create GitHub Release with:
   - Linux arm64 (Raspberry Pi)
   - Linux amd64
   - macOS arm64
   - macOS amd64

---

## 8. Post-Release Roadmap

### v0.5.0 — Fabric Integration (Next)

- [ ] `internal/fabric/` package
- [ ] Agent identity generation
- [ ] Human Root binding
- [ ] Cryptographic pairing flow

### v0.6.0 — Voice & Avatar

- [ ] Voice cloning integration
- [ ] Voice input (STT)
- [ ] Avatar generation

### v0.7.0 — Multi-Channel

- [ ] WhatsApp integration
- [ ] Signal integration
- [ ] Matrix/Element integration

---

## 9. Support

- **GitHub Issues:** https://github.com/KekwanuLabs/crayfish/issues
- **Documentation:** https://crayfish.ai/docs

---

*Accessible AI for everyone.*

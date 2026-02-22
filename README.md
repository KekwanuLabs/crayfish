<p align="center">
  <img src="assets/logo-192.png" alt="Crayfish" width="128">
</p>

# Crayfish

**Your personal AI assistant that runs on a Raspberry Pi.**

> *Accessible AI for everyone.*

Crayfish is AI that lives in your home, not someone else's cloud.

---

## What is Crayfish?

Crayfish is a personal AI assistant that:

- **Runs on a $35 Raspberry Pi** in your home
- **Connects to Telegram** so you can chat from anywhere
- **Reads and searches your Gmail** (with your permission)
- **Remembers things** you tell it across conversations
- **Learns new skills** through a simple web interface
- **Costs ~$5/year** in electricity (vs $20+/month for cloud AI)

**No cloud hosting. No monthly subscriptions. Your data stays home.**

---

## Quick Start (Non-Technical)

### What You Need

| Item | Cost | Where to Buy |
|------|------|--------------|
| Raspberry Pi 4 or 5 | $35-75 | amazon.com, raspberrypi.com |
| MicroSD Card (32GB+) | $8-15 | Any electronics store |
| Power Supply (USB-C) | $10-15 | Usually included with Pi |
| Ethernet cable OR WiFi | - | Pi 4/5 has built-in WiFi |

### Step-by-Step Setup

#### 1. Prepare Your Raspberry Pi

1. Download [Raspberry Pi Imager](https://www.raspberrypi.com/software/)
2. Insert your SD card into your computer
3. Open Raspberry Pi Imager and:
   - Choose **Raspberry Pi OS Lite (64-bit)**
   - Click the gear icon (⚙️) to configure:
     - Set hostname: `crayfish`
     - Enable SSH
     - Set username/password
     - Configure WiFi (if not using ethernet)
   - Write to your SD card
4. Insert SD card into Pi, plug in power
5. Wait 2-3 minutes for it to boot

#### 2. Install Crayfish

From your computer, open Terminal (Mac/Linux) or PowerShell (Windows):

```bash
# Connect to your Pi (replace YOUR_PASSWORD)
ssh pi@crayfish.local

# Download and run the installer
curl -fsSL https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.sh | bash
```

#### 3. Complete Setup in Your Browser

1. Open your web browser
2. Go to: `http://crayfish.local:8119`
3. Follow the setup wizard:
   - Choose your AI provider (Claude recommended)
   - Enter your API key
   - Connect Telegram (optional)
   - Connect Gmail (optional)

#### 4. Start Chatting!

Open Telegram, search for your bot, and say hello!

---

## Quick Start (Technical)

### Prerequisites

- Go 1.21+
- An LLM API key (Anthropic Claude, OpenAI, or Grok) **OR** Ollama for local inference

### Build & Run

```bash
# Clone
git clone https://github.com/KekwanuLabs/crayfish.git
cd crayfish

# Build
go build -o crayfish ./cmd/crayfish

# Run (will start setup wizard on first run)
./crayfish
```

### Environment Variables

```bash
# Required
export CRAYFISH_API_KEY="sk-ant-..."     # Your LLM API key
export CRAYFISH_PROVIDER="anthropic"      # anthropic, openai, grok

# Optional
export CRAYFISH_MODEL="claude-sonnet-4-20250514"
export CRAYFISH_LISTEN=":8119"
export CRAYFISH_TELEGRAM_TOKEN="123456:ABC..."
export CRAYFISH_GMAIL_USER="you@gmail.com"
export CRAYFISH_GMAIL_APP_PASSWORD="xxxx xxxx xxxx xxxx"
export CRAYFISH_BRAVE_API_KEY="..."       # For web search
export CRAYFISH_DEBUG=1                    # Enable debug logging
```

### On-Device LLM (Ollama) — No Cloud Required

Run Crayfish entirely offline using Ollama for local inference:

```bash
# Install Ollama on Raspberry Pi
curl -fsSL https://ollama.com/install.sh | sh

# Pull a model (choose based on your Pi's RAM)
ollama pull phi3              # 4GB RAM - recommended
ollama pull gemma:2b          # 2GB RAM - lighter
ollama pull tinyllama         # 1GB RAM - minimal

# Configure Crayfish for local LLM
export CRAYFISH_PROVIDER=ollama
export CRAYFISH_MODEL=phi3
# No API key needed!

./crayfish
```

**Recommended Models for Raspberry Pi:**

| Model | RAM | Speed | Quality |
|-------|-----|-------|---------|
| Phi-3-mini (3.8B) | 4GB | Fast | Good |
| Gemma 2B | 2GB | Very Fast | Decent |
| TinyLlama (1.1B) | 1GB | Very Fast | Basic |
| Llama 3.2 (3B) | 4GB | Fast | Good |

### Deploy to Raspberry Pi

```bash
# One command deploys to your Pi
./scripts/deploy.sh pi@192.168.1.50
```

---

## Features

### Talk to Your AI Anywhere

Chat via Telegram from your phone, tablet, or computer. Crayfish runs at home but you can reach it from anywhere.

```
You: What's in my inbox?

Crayfish: You have 3 unread emails:
1. "Meeting Tomorrow" from boss@work.com
2. "Your Order Shipped" from amazon.com
3. "Happy Birthday!" from mom@family.com
```

### Email Integration

Crayfish can search and read your Gmail:

```
You: Find emails from Amazon this month

Crayfish: Found 5 emails from Amazon:
- Order #123 shipped (Feb 15)
- Your package was delivered (Feb 12)
...
```

**Setup:** You'll need a Gmail App Password (not your regular password). The setup wizard walks you through this.

### Long-Term Memory

Crayfish remembers things across conversations:

```
You: Remember that my mom's birthday is March 15th

Crayfish: Got it! I'll remember your mom's birthday is March 15th.

[Two weeks later...]

You: When is my mom's birthday?

Crayfish: Your mom's birthday is March 15th.
```

### Skills (Teachable Behaviors)

Teach Crayfish new tricks through the web interface:

1. Open `http://crayfish.local:8119/skills`
2. Click "New Skill"
3. Fill in:
   - **Name:** `morning-briefing`
   - **Command:** `/briefing`
   - **Description:** Get my morning summary
   - **Prompt:** Summarize my emails and any important reminders

Now you can type `/briefing` in Telegram!

### Web Search

With a Brave Search API key, Crayfish can search the web:

```
You: What's the weather in Tokyo?

Crayfish: Currently in Tokyo: 18°C (64°F),
clear skies with 45% humidity.
```

---

## Security & Privacy

### Your Data Stays Home

- Crayfish runs on YOUR hardware
- Emails, memories, and conversations are stored locally
- Only LLM API calls leave your network
- No telemetry, no tracking, no cloud sync

### Identity Verification

Crayfish doesn't trust strangers:

```
Stranger: Hey, check my emails!

Crayfish: I don't recognize you.
Ask the owner for a pairing code.

[Owner generates code: 847291]

Stranger: 847291

Crayfish: Welcome! You're now connected.
```

### Trust Tiers

| Tier | Who | Can Do |
|------|-----|--------|
| **Operator** | You (first to pair) | Everything |
| **Trusted User** | Family/friends you approve | Search, read emails |
| **Group Member** | People in group chats | Ask questions only |
| **Unknown** | Everyone else | Request pairing only |

---

## Configuration

### Config File

Crayfish looks for configuration in:
1. `./crayfish.yaml` (current directory)
2. `~/.config/crayfish/crayfish.yaml`
3. `/etc/crayfish/crayfish.yaml`

Example `crayfish.yaml`:

```yaml
provider: anthropic
model: claude-sonnet-4-20250514
listen_addr: ":8119"

telegram:
  token: "123456:ABC..."

gmail:
  user: "you@gmail.com"
  app_password: "xxxx xxxx xxxx xxxx"

brave:
  api_key: "BSA..."

auto_update: true
```

### API Keys

| Provider | Get Your Key | Notes |
|----------|--------------|-------|
| **Anthropic (Claude)** | [console.anthropic.com](https://console.anthropic.com) | Recommended |
| **OpenAI** | [platform.openai.com](https://platform.openai.com) | GPT-4/5 |
| **Grok (xAI)** | [console.x.ai](https://console.x.ai) | xAI's model |
| **Telegram Bot** | [@BotFather](https://t.me/BotFather) | Create new bot |
| **Gmail App Password** | [Google Account Security](https://myaccount.google.com/apppasswords) | 2FA required |
| **Brave Search** | [brave.com/search/api](https://brave.com/search/api) | Optional |

---

## Skills Deep Dive

### Creating Skills via Web UI

1. Open `http://YOUR_PI_IP:8119/skills`
2. Click **+ New Skill**
3. Fill in the form:

| Field | Description | Example |
|-------|-------------|---------|
| Name | Unique identifier | `weekly-summary` |
| Type | workflow, prompt, or reactive | `workflow` |
| Description | What it does | "Weekly email digest" |
| Command | Slash command trigger | `/weekly` |
| Schedule | Cron expression | `0 9 * * 1` (Monday 9 AM) |
| Event | Event trigger | `email.new` |
| Prompt | What to tell the AI | "Summarize this week's emails" |

### Skill Types

**Workflow** — Multi-step process with tool calls:
```yaml
name: morning-briefing
type: workflow
trigger:
  schedule: "0 7 * * *"
  command: "/briefing"
steps:
  - tool: email_check
    params: { limit: 20 }
    store_as: emails
  - tool: web_search
    params: { query: "news today" }
    store_as: news
prompt: |
  Create my morning briefing:
  Emails: {{emails}}
  News: {{news}}
```

**Prompt** — Adds context to conversations:
```yaml
name: code-reviewer
type: prompt
trigger:
  command: "/review"
prompt: |
  You are a senior code reviewer.
  Be constructive but thorough.
  Focus on security and performance.
```

**Reactive** — Responds to events:
```yaml
name: urgent-alert
type: reactive
trigger:
  event: email.new
  keywords: ["urgent", "asap", "emergency"]
prompt: |
  An urgent email arrived.
  Summarize it immediately.
```

### Skills via YAML Files

Drop `.yaml` files in the `skills/` directory:

```bash
skills/
├── morning-briefing.yaml
├── weekly-summary.yaml
└── urgent-alert.yaml
```

Skills are loaded on startup and hot-reloaded when created via web UI.

---

## MCP (External Tools)

Connect to external services without code changes using [Model Context Protocol](https://modelcontextprotocol.io).

### Available MCP Servers

| Server | What It Does |
|--------|--------------|
| `@modelcontextprotocol/server-github` | GitHub issues, PRs |
| `@modelcontextprotocol/server-notion` | Notion pages |
| `@modelcontextprotocol/server-filesystem` | Local files |
| `@modelcontextprotocol/server-sqlite` | SQLite databases |

### Connecting MCP Servers

In your config:

```yaml
mcp:
  servers:
    - name: github
      command: "npx @modelcontextprotocol/server-github"
      enabled: true
    - name: notion
      command: "npx @modelcontextprotocol/server-notion"
      enabled: true
```

Tools become available as `github.create_issue`, `notion.search_pages`, etc.

---

## Voice (Text-to-Speech)

Give your Crayfish a voice using local neural TTS. Runs entirely on-device, no cloud required.

### Install Piper

Crayfish uses [Piper](https://github.com/OHF-Voice/piper1-gpl) — a fast, high-quality neural TTS engine maintained by Open Home Foundation.

```bash
# Install piper-tts (requires Python 3)
pip install piper-tts
```

### Install a Voice

Download a voice model using piper's built-in downloader:

```bash
# List available voices
python3 -m piper.download_voices

# Download a voice (example: Lessac, a natural US English voice)
python3 -m piper.download_voices en_US-lessac-medium
```

### Test It

```bash
# Speak to your terminal (requires ffplay)
python3 -m piper -m en_US-lessac-medium -- 'Hello, I am your Crayfish!'

# Or save to file
python3 -m piper -m en_US-lessac-medium -f hello.wav -- 'Hello, I am your Crayfish!'
```

### Enable Voice in Crayfish

In your config:

```yaml
voice_enabled: true
voice_model: "en_US-lessac-medium"
```

Or via environment:

```bash
export CRAYFISH_VOICE_ENABLED=true
export CRAYFISH_VOICE_MODEL="en_US-lessac-medium"
```

### Available Voices

Piper supports 43+ languages. Popular English voices:

| Voice | Description | Quality |
|-------|-------------|---------|
| `en_US-lessac-medium` | US English, natural | Recommended |
| `en_US-amy-medium` | US English, clear | Good |
| `en_US-ryan-medium` | US English, male | Good |
| `en_GB-alan-medium` | British English | Good |
| `en_GB-alba-medium` | Scottish English | Good |

Browse all voices: [Listen to samples](https://rhasspy.github.io/piper-samples) or [Download from HuggingFace](https://huggingface.co/rhasspy/piper-voices/tree/main)

### Performance

On Raspberry Pi 4:
- ~0.5 seconds to synthesize a short sentence
- ~50 MB memory per voice model
- Works on Pi Zero 2 W (slower, but functional)

---

## Troubleshooting

### Can't Connect to Pi

```bash
# Find your Pi's IP address
ping crayfish.local

# If that doesn't work, check your router's
# connected devices list for the Pi's IP
```

### Setup Wizard Won't Load

```bash
# SSH into Pi and check if Crayfish is running
ssh pi@crayfish.local
sudo systemctl status crayfish

# View logs
sudo journalctl -u crayfish -f
```

### Telegram Bot Not Responding

1. Make sure your bot token is correct
2. Check that you've started a conversation with the bot
3. First message might take a few seconds

### Gmail Not Working

1. Verify you're using an App Password, not your regular password
2. Make sure 2FA is enabled on your Google account
3. Check that IMAP is enabled in Gmail settings

### "I don't recognize you" Error

You need to pair with Crayfish:
1. Ask the owner to generate a pairing code
2. Send that code to the bot
3. You'll be added as a trusted user

---

## Updating Crayfish

### Auto-Update (Recommended)

Enable in config:
```yaml
auto_update: true
```

Crayfish checks daily and updates automatically.

### Manual Update

```bash
# SSH into your Pi
ssh pi@crayfish.local

# Stop Crayfish
sudo systemctl stop crayfish

# Download latest
curl -fsSL https://github.com/KekwanuLabs/crayfish/releases/latest/download/crayfish-linux-arm64 -o /usr/local/bin/crayfish
chmod +x /usr/local/bin/crayfish

# Start Crayfish
sudo systemctl start crayfish
```

---

## Architecture

See [docs/architecture.md](docs/architecture.md) for detailed technical documentation.

**TL;DR:** ~10,000 lines of Go. One binary. One SQLite file. Runs on a Raspberry Pi.

```
┌─────────────────────────────────────────┐
│            YOUR RASPBERRY PI            │
│                                         │
│  ┌─────────────────────────────────┐   │
│  │          CRAYFISH               │   │
│  │  ┌─────────┐  ┌─────────────┐   │   │
│  │  │ Gateway │  │   Runtime   │   │   │
│  │  │ (HTTP)  │  │   (Brain)   │   │   │
│  │  └────┬────┘  └──────┬──────┘   │   │
│  │       │              │          │   │
│  │       └──────┬───────┘          │   │
│  │              ▼                  │   │
│  │       ┌──────────┐              │   │
│  │       │  SQLite  │              │   │
│  │       │(storage) │              │   │
│  │       └──────────┘              │   │
│  └─────────────────────────────────┘   │
│                                         │
└────────────────────┬────────────────────┘
                     │
                     ▼ API calls only
              ┌──────────────┐
              │  Claude API  │
              │  (external)  │
              └──────────────┘
```

---

## Contributing

Contributions welcome! Please keep it simple:

- No unnecessary dependencies
- No microservices
- Every feature should work on a Pi 2

```bash
# Run tests
go test ./...

# Build
go build ./...
```

---

## License

MIT License. Use it however you want.

---

## Acknowledgments

Built with:
- [Go](https://go.dev) — Simple, fast, compiles anywhere
- [SQLite](https://sqlite.org) — The world's most deployed database
- [Anthropic Claude](https://anthropic.com) — AI that's actually helpful

---

**Accessible AI for everyone.**

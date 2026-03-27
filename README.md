<p align="center">
  <img src="assets/logo.png" alt="Crayfish" width="280">
</p>

<h1 align="center">Crayfish</h1>

<p align="center">
  <strong>Can't afford the lobster? Get the crayfish.</strong>
</p>

<p align="center">
  <em>A personal AI assistant that runs on anything from a $5 Pi Zero to a Mac Studio.<br>No PhD required. No jargon. No gatekeeping.</em>
</p>

---

## The Problem

AI assistants today are like lobster dinners — powerful, impressive, and priced for the privileged few. You need expensive hardware, technical expertise, or a monthly subscription that adds up fast.

**Crayfish is the alternative.** Same delicious AI capabilities. Runs on a $5 Pi Zero or a $5,000 workstation. Open source, built iteratively by and for everyone.

---

## What is Crayfish?

Crayfish is an AI assistant that:

- **Runs on your own hardware** — Pi Zero to Windows PC, it's your device
- **Works out of the box** — plug in, open browser, done
- **Talks to you on Telegram** from anywhere in the world
- **Manages your email and calendar** so you don't have to
- **Makes phone calls on your behalf** — "Call my wife and tell her I'll be late"
- **Checks in proactively** — "Hey, you have a meeting in 15 minutes"
- **Understands voice messages** — just talk, it transcribes and responds
- **Searches the web and checks the weather** in real time
- **Costs nothing after setup** — or pennies if using cloud AI

No command line. No config files. No "just SSH into your server and..."

You open a webpage. You fill in a form. It works.

---

## Who is this for?

**Everyone.**

- A busy parent managing the household
- A small business owner drowning in emails
- A student who wants AI without the price tag
- Someone who's heard about AI but doesn't know where to start
- A tinkerer who wants to hack on something fun
- A developer who appreciates clean, simple architecture

Crayfish removes the jargon. No "just SSH in and edit the config." No "you'll need to understand Docker." No gatekeeping.

If you've ever felt like AI was "not for people like me" — we built this for you.

And if you're technical? You'll love the codebase. It's Go, it's clean, and PRs are welcome.

---

## How it Works

### Step 1: Get Any Device

- **$5 Pi Zero** — tiny and perfect
- **$35 Pi 4** — the sweet spot
- **Old laptop** — give it new life
- **Mac** (Intel or Apple Silicon) — works great too
- **Windows PC** — native support, no WSL needed
- **Linux PC/server** — Debian/Ubuntu-based (apt)
- **Cloud server** — if that's your thing

Got old hardware collecting dust? Perfect.

### Step 2: Install Crayfish

**Option A: Direct Install (Recommended)**

Run this on the device you want Crayfish to live on:

*Linux / macOS:*
```bash
curl -fsSL https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.sh | bash
```

*Windows (PowerShell):*
```powershell
# Allow running scripts (one-time, if not already set)
Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned

# Install Crayfish
iwr https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.ps1 | iex
```

That's it. The installer downloads everything, sets it up, and starts it automatically.

**Option B: Install Remotely from Another Computer (Pi/headless devices)**

Don't want to plug a keyboard into your Pi? Install remotely from your Mac or PC:

1. Make sure your Pi is on and connected to your network
2. Find your Pi's IP address (check your router, or it's often `raspberrypi.local`)
3. Run this on your Mac/PC:

```bash
git clone https://github.com/KekwanuLabs/crayfish.git
cd crayfish
make deploy
```

The first time, it will ask you:
- **IP address** — Where is your Pi? (e.g., `192.168.1.42` or `raspberrypi.local`)
- **Username** — Usually `pi` for Raspberry Pi
- **Device type** — What kind of Pi is it?

It remembers your answers, so next time you just run `make deploy` again.

> **Note:** You'll need Go installed on your Mac/PC to build. On Mac: `brew install go`. On Ubuntu/Debian: `sudo apt install golang`.

### Step 3: Open the Setup Wizard

Once installed, open your web browser and go to:

- **Same device (Windows, Mac, Linux PC):** `http://localhost:8119`
- **Pi or remote device:** `http://your-device-ip:8119` (e.g. `http://192.168.1.42:8119` or `http://raspberrypi.local:8119`)

### Step 4: Follow the Wizard

<p align="center">
  <em>Point. Click. Done.</em>
</p>

The wizard walks you through:
- Giving your Crayfish a name (it's *your* assistant)
- Connecting your AI brain (Anthropic, OpenAI, or free local models)
- Setting up Telegram (so you can chat from anywhere)
- Linking Gmail & Calendar (optional, for email/scheduling magic)

No config files. No command line. Just fill in the blanks.

---

## Features

### Chat from Anywhere
Connect via Telegram. Ask questions, give commands, have conversations — all from your phone.

### Email & Calendar
"Send an email to Mom saying I'll be late."
"What's on my calendar tomorrow?"
"Schedule lunch with Alex on Friday at noon."

### Proactive Check-ins
During work hours, Crayfish checks your email and calendar and nudges you:
> "Hey! Quick check-in:
> - 📧 2 urgent emails need your attention
> - 📅 Team standup starts in 15 minutes"

### Learns About You
On first conversation, Crayfish naturally asks about you — your name, what you do, how you like to communicate. It saves this as a local profile so every interaction is personal, not generic. You can also shape its personality over time — "be more casual" or "keep responses short." All stored locally, never sent to a cloud.

### Voice Messages
Send a voice note on Telegram. Crayfish transcribes it and responds. No typing required.

Voice transcription works automatically on all platforms. If you're already using Groq or OpenAI as your AI provider, the same key handles voice too — nothing extra to set up. On a Pi 3+ it can run fully local (offline). On other hardware it uses free cloud transcription.

### Admin Dashboard
Manage everything from your browser at `http://localhost:8119` (or your Pi's IP). View sessions, search memories, configure settings, manage contacts, manage skills, and monitor events — all from a single page. Settings apply instantly; provider changes show a "restart needed" indicator.

### Privacy First
Your data lives on your device, in your home. Conversations aren't training someone else's AI. You own everything.

### Phone Calls
"Call my wife and tell her I'll be late." Crayfish dials the number, speaks the message in your configured voice, and handles the conversation. Powered by Twilio — set up once in the dashboard, works automatically after that.

### Real-Time Weather
"Will it rain in Orcas Island today?" Instant, accurate weather from Open-Meteo — free, no API key, works anywhere in the world.

### Works Offline (Optional)
Got a beefier Pi or don't want to pay for cloud AI? Run local models with Ollama. Completely offline, completely free.

---

## The Lobster vs Crayfish Comparison

| | Lobster (Premium AI) | Crayfish |
|---|---|---|
| **Hardware** | "You need a good machine" | $5 Pi Zero to Mac Studio — anything works |
| **Setup** | Hours of configuration | 5 minutes, browser-based |
| **Technical skill** | "Just edit the YAML and..." | None. Point, click, done. |
| **Monthly cost** | $20-100/month | $0-5/month |
| **Your data** | In their cloud | On your device |
| **Runs 24/7** | Drains your laptop | 2-5W on a Pi; runs as a service on PC |
| **Community** | "Read the docs" | Built for everyone, with everyone |

---

## Frequently Asked Questions

### Do I need to know how to code?
No. If you can use a web browser, you can set up Crayfish.

### What hardware do I need?
Anything. Pi Zero, Pi 2/3/4/5, old laptop, Mac, PC, cloud server. More RAM = can run local AI models. But even a $5 Pi Zero works great with cloud AI.

### How do I find my Pi's IP address?
A few ways:
- **Check your router** — Look for "Raspberry Pi" in the connected devices list
- **Try `raspberrypi.local`** — Works on most home networks
- **On the Pi itself** — Run `hostname -I` if you have a keyboard connected

### Is it really free?
The software is free and open source. You pay for:
- Hardware if you don't have any (a Pi 4 is ~$35–80 one-time; a Windows/Mac you likely already own)
- Cloud AI usage (~$0–5/month depending on use) OR
- Nothing if you run local models with Ollama

### What about privacy?
Your device, your data. Conversations, emails, and calendar data stay on your hardware — whether that's a Pi in your home or your Windows laptop. We don't run any cloud services. There's nothing to phone home to.

### Can I use it without Telegram?
Yes! There's a web interface and CLI too. But Telegram is the magic — it means you can chat with your Crayfish from anywhere.

### How do I update Crayfish?
Crayfish updates itself automatically by default. You don't need to do anything.

If you installed via `make deploy`, just run it again to push the latest version.

---

## For Developers

Want to hack on Crayfish? Welcome!

### Build from Source

```bash
git clone https://github.com/KekwanuLabs/crayfish.git
cd crayfish
make build               # Build for your current machine
make run                 # Build and run locally
make build-windows-amd64 # Cross-compile for Windows (produces .exe)
make build-all           # All platforms: Linux, macOS, Windows
```

### Deploy to a Pi

```bash
make deploy     # Cross-compile and push to your Pi
```

First run prompts for:
- Pi's IP address (e.g., `192.168.1.42`)
- SSH username (usually `pi`)
- Architecture (`arm64` for Pi 3/4/5, `armv7` for Pi 2, `armv6` for Pi Zero)

Settings are saved to `.deploy.env` — delete it to reconfigure.

### Fresh Install (Wipe Everything)

```bash
make deploy-clean   # Wipes data on Pi, then deploys fresh
```

### Project Structure

```
cmd/crayfish/           # Main entry point
internal/               # Core packages
  app/                  # Application orchestration & config
  bus/                  # Event bus (SQLite-backed)
  channels/             # Channel adapters (Telegram, CLI, Phone/SMS)
  contacts/             # Private contacts store (phone book)
  device/               # Hardware detection (Pi model, RAM, arch)
  firewall/             # Cross-platform firewall management (ufw/netsh)
  gateway/              # HTTP server, dashboard, skills API
  gmail/                # Email integration (IMAP + OAuth)
  calendar/             # Google Calendar
  heartbeat/            # Proactive check-ins
  identity/             # Agent personality + user knowledge (SOUL.md, USER.md)
  oauth/                # Google OAuth2 device flow (Calendar, Drive, Contacts)
  provider/             # LLM providers (Anthropic, OpenAI, Groq, Ollama, etc.)
  runtime/              # Agent brain, tool execution, memory
  security/             # Trust tiers, pairing, guardrails
  setup/                # Web setup wizard
  skills/               # YAML-defined workflows
  storage/              # SQLite wrapper
  tools/                # Built-in tool registry (60+ tools)
  tunnel/               # Cloudflare Tunnel manager (auto phone webhook sync)
  voice/                # TTS (Piper, ElevenLabs) + STT (whisper.cpp, cloud)
scripts/                # Install scripts (install.sh, install.ps1, deploy.sh)
docs/                   # Architecture & network documentation
```

### Contributing

PRs welcome! The codebase is intentionally simple — no frameworks, minimal dependencies, easy to understand.

---

## Quick Links

- **Website:** [crayfish-ai.com](https://crayfish-ai.com/)
- **Install (Linux/Mac):** `curl -fsSL https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.sh | bash`
- **Install (Windows):** `iwr https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.ps1 | iex`
- **Install from laptop (Pi/headless):** `git clone ... && make deploy`
- **Issues:** [GitHub Issues](https://github.com/KekwanuLabs/crayfish/issues)
- **License:** MIT

---

<p align="center">
  <strong>Can't afford the lobster? Get the crayfish.</strong><br>
  <em>Open source. Built for everyone, with everyone.</em>
</p>

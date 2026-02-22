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

- **Runs on a Raspberry Pi** sitting in your home
- **Works out of the box** — plug in, open browser, done
- **Talks to you on Telegram** from anywhere in the world
- **Manages your email and calendar** so you don't have to
- **Checks in proactively** — "Hey, you have a meeting in 15 minutes"
- **Understands voice messages** — just talk, it transcribes
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

### 1. Get Any Device

- **$5 Pi Zero** — tiny and perfect
- **$35 Pi 4** — the sweet spot
- **Old laptop** — give it new life
- **Mac or PC** — works great too
- **Cloud server** — if that's your thing

Got old hardware collecting dust? Perfect.

### 2. Install Crayfish

```bash
curl -fsSL https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.sh | bash
```

### 3. Open Your Browser

Navigate to `http://your-pi-ip:8119` from your phone or laptop.

### 4. Follow the Setup Wizard

<p align="center">
  <em>Point. Click. Done.</em>
</p>

The wizard walks you through:
- Giving your Crayfish a name (it's *your* assistant)
- Connecting your AI brain (Anthropic, OpenAI, or free local models)
- Setting up Telegram (so you can chat from anywhere)
- Linking Gmail & Calendar (optional, for email/scheduling magic)

No terminal. No YAML files. No documentation rabbit holes.

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

### Voice Messages
Send a voice note on Telegram. Crayfish transcribes it and responds. No typing required.

### Privacy First
Your data lives on your Pi, in your home. Conversations aren't training someone else's AI. You own everything.

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
| **Runs 24/7** | Drains your laptop | Sips 2-5 watts |
| **Community** | "Read the docs" | Built for everyone, with everyone |

---

## Frequently Asked Questions

### Do I need to know how to code?
No. If you can use a web browser, you can set up Crayfish.

### What hardware do I need?
Anything. Pi Zero, Pi 2/3/4/5, old laptop, Mac, PC, cloud server. More RAM = can run local AI models. But even a $5 Pi Zero works great with cloud AI.

### Is it really free?
The software is free and open source. You pay for:
- A Raspberry Pi (~$35-80 one-time)
- Cloud AI usage (~$0-5/month depending on use) OR
- Nothing extra if you run local models

### What about privacy?
Your Pi sits in your home. Your conversations, emails, and calendar data stay on your Pi. We don't run any cloud services. There's nothing to phone home to.

### Can I use it without Telegram?
Yes! There's a web interface and CLI too. But Telegram is the magic — it means you can chat with your Crayfish from anywhere.

### I'm technical, can I still use this?
Absolutely. It's written in Go, fully open source, and designed with clean architecture. Hack away. PRs welcome.

---

## Quick Links

- **Install:** `curl -fsSL https://raw.githubusercontent.com/KekwanuLabs/crayfish/main/scripts/install.sh | bash`
- **Documentation:** [Coming soon]
- **Issues:** [GitHub Issues](https://github.com/KekwanuLabs/crayfish/issues)
- **License:** MIT

---

<p align="center">
  <strong>Can't afford the lobster? Get the crayfish.</strong><br>
  <em>Open source. Built for everyone, with everyone.</em>
</p>

<p align="center">
  <img src="assets/logo.png" alt="Crayfish" width="280">
</p>

<h1 align="center">Crayfish</h1>

<p align="center">
  <strong>Can't afford the lobster? Get the crayfish.</strong>
</p>

<p align="center">
  <em>A personal AI assistant that runs on a $35 Raspberry Pi.<br>No PhD required. No cloud bills. No nonsense.</em>
</p>

---

## The Problem

AI assistants today are like lobster dinners — powerful, impressive, and priced for the privileged few. You need expensive hardware, technical expertise, or a monthly subscription that adds up fast.

**Crayfish is the $35 alternative.** Same delicious AI capabilities, fraction of the cost, and you don't need to be a chef to enjoy it.

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

**Crayfish is for people who want AI to help them, not become their second job.**

You might be:
- A busy parent who wants help managing the household
- A small business owner drowning in emails
- A student who wants a smart assistant without the smart price
- Someone who's heard about AI but doesn't know where to start
- A person who values privacy and wants their data at home

If you've ever felt like AI was "not for people like me" — Crayfish is for you.

*(Tech folks welcome too. You'll appreciate the clean architecture.)*

---

## How it Works

### 1. Get a Raspberry Pi

Any model works. A $35 Pi 4 is perfect. Got an old Pi collecting dust? Even better.

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
| **Hardware** | $2000+ Mac/PC | $35 Raspberry Pi |
| **Setup** | Hours of configuration | 5 minutes, browser-based |
| **Technical skill** | High | None |
| **Monthly cost** | $20-100/month | $0-5/month |
| **Your data** | In their cloud | In your home |
| **Runs 24/7** | Drains your laptop | Sips 5 watts |

---

## Frequently Asked Questions

### Do I need to know how to code?
No. If you can use a web browser, you can set up Crayfish.

### What Raspberry Pi do I need?
Any Pi 2, 3, 4, or 5 works. More RAM = can run local AI models. But even a basic Pi works great with cloud AI.

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
  <em>Accessible AI for everyone.</em>
</p>

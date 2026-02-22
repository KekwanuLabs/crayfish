# Crayfish Architecture

> **Accessible AI for everyone.** — A personal AI that runs on a $35 computer.

## Philosophy

Crayfish is intentionally simple. While other AI assistants ship 400,000+ lines of code with complex microservices, Crayfish is ~10,000 lines of Go that runs on a Raspberry Pi. No Kubernetes. No Docker. No cloud dependency.

```
┌─────────────────────────────────────────────────────────────────┐
│                     CRAYFISH vs THE BLOAT                       │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│   OpenClaw-style       │        Crayfish                        │
│   ─────────────        │        ────────                        │
│   400,000 lines        │        ~10,000 lines                   │
│   20+ microservices    │        1 binary                        │
│   Redis + Postgres     │        SQLite                          │
│   + Kafka + Docker     │        (single file)                   │
│   Needs 8GB+ RAM       │        Runs on 512MB                   │
│   $100+/month cloud    │        $5/year electricity             │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

---

## System Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              CRAYFISH                                    │
│                         Your Personal AI                                 │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ╔═══════════════════════════════════════════════════════════════════╗  │
│  ║                      CHANNELS (Input/Output)                       ║  │
│  ╠═══════════╦═══════════╦═══════════╦═══════════╦═══════════════════╣  │
│  ║ Telegram  ║   CLI     ║  WhatsApp ║   Email   ║   Web Browser     ║  │
│  ║    📱     ║    💻     ║    💬     ║    📧     ║       🌐          ║  │
│  ╚═════╦═════╩═════╦═════╩═════╦═════╩═════╦═════╩═════════╦═════════╝  │
│        │           │           │           │               │            │
│        └───────────┴─────┬─────┴───────────┘               │            │
│                          ▼                                 │            │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │                         GATEWAY                                   │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐   │  │
│  │  │ HTTP Server │  │  Event Bus  │  │    Channel Adapters     │   │  │
│  │  │  :8119      │  │  (SQLite)   │  │  (route msgs in/out)    │   │  │
│  │  └──────┬──────┘  └──────┬──────┘  └────────────┬────────────┘   │  │
│  └─────────┼────────────────┼──────────────────────┼────────────────┘  │
│            │                │                      │                   │
│            │    ┌───────────┴───────────┐          │                   │
│            │    ▼                       ▼          │                   │
│  ┌─────────┴────────────────────────────────────────────────────────┐  │
│  │                          RUNTIME                                  │  │
│  │                                                                   │  │
│  │   Message ──▶ Session ──▶ Context ──▶ LLM ──▶ Tools ──▶ Reply    │  │
│  │      │          │           │          │        │         │       │  │
│  │      │     ┌────┴────┐ ┌────┴────┐ ┌───┴───┐ ┌──┴──┐     │       │  │
│  │      │     │Identity │ │ Memory  │ │Claude │ │Email│     │       │  │
│  │      │     │ Check   │ │Retrieval│ │OpenAI │ │Gmail│     │       │  │
│  │      │     │(pairing)│ │ (FTS5)  │ │ Grok  │ │Web  │     │       │  │
│  │      │     └─────────┘ └─────────┘ └───────┘ └─────┘     │       │  │
│  │      │                                                    │       │  │
│  └──────┼────────────────────────────────────────────────────┼───────┘  │
│         │                                                    │          │
│         ▼                                                    ▼          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │                         STORAGE                                   │  │
│  │                      (Single SQLite File)                         │  │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────────┐  │  │
│  │  │ Events  │ │Sessions │ │ Memory  │ │Messages │ │   Skills    │  │  │
│  │  │  (WAL)  │ │ (Trust) │ │ (FTS5)  │ │ (Cache) │ │   (YAML)    │  │  │
│  │  └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────────┘  │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Three-Layer Architecture

### Layer 1: Gateway (Always Running)

The gateway is the front door. It runs 24/7, uses minimal resources, and handles:

```
┌─────────────────────────────────────────────────────────────┐
│                         GATEWAY                              │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  HTTP Server (:8119)                                        │
│  ├── /health         → Is Crayfish alive?                  │
│  ├── /status         → What adapters are running?          │
│  ├── /skills         → Web UI for managing skills          │
│  └── /api/skills/*   → REST API for skills                 │
│                                                             │
│  Channel Adapters                                           │
│  ├── Telegram        → Your phone/desktop Telegram         │
│  ├── CLI             → Terminal interface                  │
│  └── (WhatsApp)      → Coming soon                         │
│                                                             │
│  Event Bus (CrayfishBus)                                    │
│  └── All messages flow through here as events              │
│      Stored in SQLite for replay/debugging                 │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Layer 2: Runtime (The Brain)

The runtime processes messages and calls the AI:

```
┌─────────────────────────────────────────────────────────────┐
│                         RUNTIME                              │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐  │
│  │ Message │───▶│ Session │───▶│ Context │───▶│   LLM   │  │
│  │ Arrives │    │ Lookup  │    │ Builder │    │  Call   │  │
│  └─────────┘    └─────────┘    └─────────┘    └────┬────┘  │
│                      │              │              │        │
│                      ▼              ▼              ▼        │
│                 ┌─────────┐   ┌─────────┐   ┌──────────┐   │
│                 │ Trust   │   │ Memory  │   │  Tool    │   │
│                 │ Check   │   │ Inject  │   │Execution │   │
│                 │(Tier?)  │   │(FTS5)   │   │(sandbox) │   │
│                 └─────────┘   └─────────┘   └──────────┘   │
│                                                             │
│  Tool Registry                                              │
│  ├── email_search    → Search your Gmail                   │
│  ├── email_read      → Read specific emails                │
│  ├── email_send      → Send emails (trusted users only)    │
│  ├── web_search      → Search the web (Brave)              │
│  ├── memory_store    → Remember something                  │
│  ├── memory_recall   → Recall past information             │
│  └── mcp_*           → External MCP tools                  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Layer 3: Storage (Everything in SQLite)

One database file. No external services.

```
┌─────────────────────────────────────────────────────────────┐
│                    STORAGE (SQLite)                          │
│                    📁 crayfish.db                            │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ events              │ The event log (CrayfishBus)   │   │
│  │                     │ Every message, tool call,     │   │
│  │                     │ system event is recorded      │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ sessions            │ Who is this person?           │   │
│  │                     │ What's their trust level?     │   │
│  │                     │ When did they pair?           │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ memory_fts          │ Long-term memory (FTS5)       │   │
│  │                     │ "Remember: Mom's birthday     │   │
│  │                     │  is March 15th"               │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ gmail_cache         │ Cached emails for search      │   │
│  │                     │ (indexed with FTS5)           │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## Security Model

Trust is earned, not assumed. Every user starts as "Unknown" and must prove who they are.

```
┌─────────────────────────────────────────────────────────────┐
│                     TRUST TIERS                              │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ 👑 OPERATOR (You)                                   │   │
│  │    • Full access to everything                      │   │
│  │    • Can pair new devices                           │   │
│  │    • Can run any tool                               │   │
│  │    • First Telegram user to pair                    │   │
│  └─────────────────────────────────────────────────────┘   │
│                          ▲                                  │
│                          │                                  │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ 👤 TRUSTED USER                                     │   │
│  │    • Can read emails, search web                    │   │
│  │    • Can use approved tools                         │   │
│  │    • Cannot change settings                         │   │
│  │    • Paired via OTP by operator                     │   │
│  └─────────────────────────────────────────────────────┘   │
│                          ▲                                  │
│                          │                                  │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ 👥 GROUP MEMBER                                     │   │
│  │    • Read-only access                               │   │
│  │    • Can ask questions                              │   │
│  │    • Cannot trigger sensitive tools                 │   │
│  └─────────────────────────────────────────────────────┘   │
│                          ▲                                  │
│                          │                                  │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ ❓ UNKNOWN                                          │   │
│  │    • Can only request pairing                       │   │
│  │    • "Who are you? Get an OTP from the owner."      │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Pairing Flow

```
  Unknown User                    Crayfish                     Operator
       │                              │                            │
       │  "Hey Crayfish!"             │                            │
       ├─────────────────────────────▶│                            │
       │                              │                            │
       │  "I don't know you.          │                            │
       │   Ask the owner for a code." │                            │
       │◀─────────────────────────────┤                            │
       │                              │                            │
       │                              │  "Generate OTP for         │
       │                              │   new family member"       │
       │                              │◀───────────────────────────┤
       │                              │                            │
       │                              │  "OTP: 847291              │
       │                              │   Valid for 10 minutes"    │
       │                              ├───────────────────────────▶│
       │                              │                            │
       │  "847291"                    │         (tells user)       │
       ├─────────────────────────────▶│◀ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─│
       │                              │                            │
       │  "Welcome! You're now        │                            │
       │   a trusted user."           │                            │
       │◀─────────────────────────────┤                            │
       │                              │                            │
```

---

## Skills System

Skills are reusable behaviors that extend Crayfish's capabilities.

```
┌─────────────────────────────────────────────────────────────┐
│                        SKILLS                                │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Trigger Types:                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ /command    │ User types "/briefing"                │   │
│  │ schedule    │ Cron: "0 7 * * *" (daily at 7 AM)     │   │
│  │ event       │ "email.new" → when new email arrives  │   │
│  │ keywords    │ ["urgent", "asap"] in message         │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  Skill Types:                                               │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ workflow    │ Multi-step: fetch data → process →    │   │
│  │             │ summarize → send                      │   │
│  │ prompt      │ Inject context into AI conversation   │   │
│  │ reactive    │ Respond to specific events            │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  Example: Morning Briefing Skill                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ name: morning-briefing                              │   │
│  │ trigger:                                            │   │
│  │   schedule: "0 7 * * *"    # 7 AM daily            │   │
│  │   command: "/briefing"     # or manual trigger     │   │
│  │ steps:                                              │   │
│  │   - tool: email_check                               │   │
│  │     params: { limit: 20 }                           │   │
│  │     store_as: emails                                │   │
│  │   - tool: web_search                                │   │
│  │     params: { query: "{{topic}} news today" }       │   │
│  │     store_as: news                                  │   │
│  │ prompt: |                                           │   │
│  │   Summarize my morning:                             │   │
│  │   Emails: {{emails}}                                │   │
│  │   News: {{news}}                                    │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Web UI for Skills

Non-technical users can create skills through a web browser:

```
┌─────────────────────────────────────────────────────────────┐
│  Crayfish Skills                           [+ New Skill]    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ morning-briefing                         [workflow] │   │
│  │ Daily summary of emails and news                    │   │
│  │ Schedule: 0 7 * * *    Command: /briefing          │   │
│  │                                    [View] [Delete]  │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ urgent-alert                            [reactive]  │   │
│  │ Notify immediately on urgent emails                 │   │
│  │ Event: email.new    Keywords: urgent, asap         │   │
│  │                                    [View] [Delete]  │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## MCP (Model Context Protocol)

MCP lets Crayfish connect to external tools without code changes.

```
┌─────────────────────────────────────────────────────────────┐
│                           MCP                                │
│              Connect to External Tool Servers                │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Crayfish ◀──────▶ MCP Server ◀──────▶ External Service    │
│                                                             │
│  Examples:                                                  │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ GitHub MCP      │ Create issues, PRs, read repos    │   │
│  │ Notion MCP      │ Search/create pages               │   │
│  │ Filesystem MCP  │ Read/write local files            │   │
│  │ Database MCP    │ Query databases                   │   │
│  │ Calendar MCP    │ Manage calendar events            │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  Transport:                                                 │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ stdio   │ Launch subprocess, communicate via stdin  │   │
│  │ HTTP    │ Connect to remote HTTP endpoint           │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
│  Tool naming: "server.tool_name"                            │
│  Example: "github.create_issue", "notion.search_pages"      │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## Message Flow

What happens when you send "What's in my inbox?" via Telegram:

```
┌──────────────────────────────────────────────────────────────────────────┐
│                           MESSAGE FLOW                                    │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  📱 You (Telegram)                                                       │
│      │                                                                   │
│      │ "What's in my inbox?"                                             │
│      ▼                                                                   │
│  ┌────────────────┐                                                      │
│  │ Telegram Bot   │ ◀── Telegram servers push update                    │
│  │ Adapter        │                                                      │
│  └───────┬────────┘                                                      │
│          │                                                               │
│          │ Publish: message.inbound                                      │
│          ▼                                                               │
│  ┌────────────────┐                                                      │
│  │  CrayfishBus   │ ◀── Event stored in SQLite                          │
│  │  (Event Log)   │                                                      │
│  └───────┬────────┘                                                      │
│          │                                                               │
│          │ Runtime subscribes to inbound events                          │
│          ▼                                                               │
│  ┌────────────────┐                                                      │
│  │    Runtime     │                                                      │
│  │                │                                                      │
│  │  1. Session?   │──▶ Look up Telegram user ID → Found: Operator       │
│  │                │                                                      │
│  │  2. Context    │──▶ Load last 10 messages + memory                   │
│  │                │                                                      │
│  │  3. LLM Call   │──▶ Send to Claude API                               │
│  │                │                                                      │
│  │  4. Response:  │◀── Claude says: "Use email_search tool"             │
│  │     Tool Call  │                                                      │
│  │                │                                                      │
│  │  5. Execute    │──▶ email_search({ limit: 10 })                      │
│  │     Tool       │◀── Returns: [email1, email2, ...]                   │
│  │                │                                                      │
│  │  6. LLM Call   │──▶ Send tool result to Claude                       │
│  │     #2         │◀── Claude: "You have 3 unread emails..."            │
│  │                │                                                      │
│  │  7. Publish    │                                                      │
│  │     Response   │                                                      │
│  └───────┬────────┘                                                      │
│          │                                                               │
│          │ Publish: message.outbound                                     │
│          ▼                                                               │
│  ┌────────────────┐                                                      │
│  │  CrayfishBus   │                                                      │
│  └───────┬────────┘                                                      │
│          │                                                               │
│          │ Gateway routes to Telegram adapter                            │
│          ▼                                                               │
│  ┌────────────────┐                                                      │
│  │ Telegram Bot   │──▶ Send message via Telegram API                    │
│  │ Adapter        │                                                      │
│  └────────────────┘                                                      │
│          │                                                               │
│          ▼                                                               │
│  📱 You see: "You have 3 unread emails: ..."                            │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## Performance Budget

Crayfish is designed to run on minimal hardware:

```
┌─────────────────────────────────────────────────────────────┐
│                   PERFORMANCE TARGETS                        │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Memory:        < 128 MB RSS                                │
│  Binary Size:   < 20 MB                                     │
│  Cold Start:    < 3 seconds                                 │
│  Per Message:   < 50 KB overhead                            │
│  Database:      Compacted at 500 MB                         │
│                                                             │
│  Target Hardware:                                           │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ Raspberry Pi 2/3/4/5                                │   │
│  │ 512 MB - 8 GB RAM                                   │   │
│  │ Any SD card (8 GB+)                                 │   │
│  │ WiFi or Ethernet                                    │   │
│  │ ~$35-75 total cost                                  │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## Directory Structure

```
crayfish/
├── cmd/
│   └── crayfish/
│       └── main.go           # Entry point
├── internal/
│   ├── app/                  # Application orchestration
│   ├── bus/                  # Event bus (CrayfishBus)
│   ├── channels/             # Channel adapters
│   │   ├── telegram/         # Telegram bot
│   │   └── cli/              # Terminal interface
│   ├── gateway/              # HTTP server, routing
│   ├── gmail/                # Gmail IMAP integration
│   ├── mcp/                  # MCP client (~200 lines)
│   ├── provider/             # LLM providers (Claude, OpenAI, Grok)
│   ├── runtime/              # Agent runtime, tool execution
│   ├── security/             # Sessions, pairing, trust tiers
│   ├── skills/               # Skills system
│   ├── storage/              # SQLite wrapper
│   └── tools/                # Built-in tools
├── scripts/
│   └── deploy.sh             # One-touch Pi deployment
├── skills/                   # User-defined skills (YAML)
└── docs/
    └── architecture.md       # This file
```

---

## Design Principles

1. **Single Binary** — No containers, no orchestration, `go build` and run
2. **Single Database** — SQLite handles events, sessions, memory, cache
3. **Offline-First** — Queue messages when LLM is unreachable
4. **Security by Default** — Unknown users can't do anything
5. **Observable** — Every event is logged and replayable
6. **Extensible** — Skills and MCP for adding capabilities without code

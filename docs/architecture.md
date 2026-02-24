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
┌────────────────────────────────────────────────────────────────────────┐
│                              CRAYFISH                                  │
│                         Your Personal AI                               │
├────────────────────────────────────────────────────────────────────────┤
│                                                                        │
│  ╔══════════════════════════════════════════════════════════════════╗  │
│  ║                      CHANNELS (Input/Output)                     ║  │
│  ╠═══════════╦═══════════╦═══════════╦═══════════╦══════════════════╣  │
│  ║ Telegram  ║   CLI     ║  WhatsApp ║   Email   ║   Web Browser    ║  │
│  ║    📱     ║    💻      ║    💬     ║    📧      ║       🌐         ║  │
│  ╚═════╦════╩═════╦═════╩═════╦═════╩═════╦═════╩═════════╦═════════╝  │
│        │           │           │           │               │           │
│        └───────────┴─────┬─────┴───────────┘               │           │
│                          ▼                                 │           │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │                         GATEWAY                                  │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐   │  │
│  │  │ HTTP Server │  │  Event Bus  │  │    Channel Adapters     │   │  │
│  │  │  :8119      │  │  (SQLite)   │  │  (route msgs in/out)    │   │  │
│  │  └──────┬──────┘  └──────┬──────┘  └────────────┬────────────┘   │  │
│  └─────────┼────────────────┼──────────────────────┼────────────────┘  │
│            │                │                      │                   │
│            │    ┌───────────┴───────────┐          │                   │
│            │    ▼                       ▼          │                   │
│  ┌─────────┴────────────────────────────────────────────────────────┐  │
│  │                          RUNTIME                                 │  │
│  │                                                                  │  │
│  │   Message ──▶ Session ──▶ Context ──▶ LLM ──▶ Tools ──▶ Reply    │  │
│  │      │          │           │          │        │         │      │  │
│  │      │     ┌────┴────┐ ┌────┴────┐ ┌───┴───┐ ┌──┴──┐      │      │  │
│  │      │     │Identity │ │ Memory  │ │Claude │ │Email│      │      │  │
│  │      │     │ Check   │ │Retrieval│ │OpenAI │ │Gmail│      │      │  │
│  │      │     │(pairing)│ │ (FTS5)  │ │ Grok  │ │Web  │      │      │  │
│  │      │     └─────────┘ └─────────┘ └───────┘ └─────┘      │      │  │
│  │      │                                                    │      │  │
│  └──────┼────────────────────────────────────────────────────┼──────┘  │
│         │                                                    │         │
│         ▼                                                    ▼         │
│  ┌───────────────────────────────────────────────────────────────────┐ │
│  │                         STORAGE                                   │ │
│  │                      (Single SQLite File)                         │ │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌───────────┐  │ │
│  │  │ Events  │ │Sessions │ │ Memory  │ │Messages │ │  Emails   │  │ │
│  │  │  (WAL)  │ │ (Trust) │ │ (FTS5)  │ │ (Cache) │ │  (FTS5)   │  │ │
│  │  └─────────┘ └─────────┘ └─────────┘ └─────────┘ └───────────┘  │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                                                        │
│  ┌───────────────────────────────────────────────────────────────────┐ │
│  │                     FILES (alongside config)                      │ │
│  │  ┌───────────┐ ┌───────────┐ ┌──────────────────────────────┐    │ │
│  │  │  SOUL.md  │ │  USER.md  │ │ skills/*.yaml (YAML on disk) │    │ │
│  │  └───────────┘ └───────────┘ └──────────────────────────────┘    │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                                                        │
└────────────────────────────────────────────────────────────────────────┘
```

---

## Three-Layer Architecture

### Layer 1: Gateway (Always Running)

The gateway is the front door. It runs 24/7, uses minimal resources, and handles:

```
┌─────────────────────────────────────────────────────────────┐
│                         GATEWAY                             │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  HTTP Server (:8119)                                        │
│  ├── /              → Admin dashboard (tabbed SPA)          │
│  ├── /health         → Is Crayfish alive?                   │
│  ├── /status         → What adapters are running?           │
│  ├── /skills         → Web UI for managing skills           │
│  ├── /api/skills/*   → REST API for skills                  │
│  └── /api/dashboard/*→ Dashboard API (config, sessions,     │
│                         memory, events, snapshots)          │
│                                                             │
│  Channel Adapters                                           │
│  ├── Telegram        → Your phone/desktop Telegram          │
│  ├── CLI             → Terminal interface                   │
│  └── (WhatsApp)      → Coming soon                          │
│                                                             │
│  Event Bus (CrayfishBus)                                    │
│  └── All messages flow through here as events               │
│      Stored in SQLite for replay/debugging                  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Layer 2: Runtime (The Brain)

The runtime processes messages and calls the AI:

```
┌────────────────────────────────────────────────────────────┐
│                         RUNTIME                            │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐  │
│  │ Message │───▶│ Session │───▶│ Context │───▶│   LLM   │  │
│  │ Arrives │    │ Lookup  │    │ Builder │    │   Call  │  │
│  └─────────┘    └─────────┘    └─────────┘    └────┬────┘  │
│                      │              │              │       │
│                      ▼              ▼              ▼       │
│                 ┌─────────┐   ┌─────────┐   ┌──────────┐   │
│                 │ Trust   │   │ Memory  │   │  Tool    │   │
│                 │ Check   │   │ Inject  │   │Execution │   │
│                 │(Tier?)  │   │(FTS5)   │   │(sandbox) │   │
│                 └─────────┘   └─────────┘   └──────────┘   │
│                                                            │
│  Tool Registry                                             │
│  ├── email_search    → Search your Gmail                   │
│  ├── email_read      → Read specific emails                │
│  ├── email_send      → Send emails (trusted users only)    │
│  ├── web_search      → Search the web (Brave)              │
│  ├── memory_store    → Remember something                  │
│  ├── memory_recall   → Recall past information             │
│  ├── identity_read   → Read agent/human identity files     │
│  ├── identity_update → Update agent/human identity files   │
│  └── mcp_*           → External MCP tools                  │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

### Layer 3: Storage (Everything in SQLite)

One database file. No external services.

```
┌────────────────────────────────────────────────────────────┐
│                    STORAGE (SQLite)                        │
│                    📁 crayfish.db                          │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ events              │ Append-only event log         │   │
│  │                     │ (messages, tools, system)     │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ sessions            │ User sessions with trust tier │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ messages            │ Conversation history          │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ memory_fts +        │ Long-term memory (FTS5) with  │   │
│  │ memory_metadata     │ categories and importance     │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ session_snapshots   │ Continuity across sessions    │   │
│  │                     │ (task, tone, proposals)       │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ emails + emails_fts │ Cached emails (FTS5 indexed)  │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

---

## Security Model

Trust is earned, not assumed. Every user starts as "Unknown" and must prove who they are.

```
┌─────────────────────────────────────────────────────────────┐
│                     TRUST TIERS                             │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ 👑 OPERATOR (You)                                   │    │
│  │    • Full access to everything                      │    │
│  │    • Can pair new devices                           │    │
│  │    • Can run any tool                               │    │
│  │    • First Telegram user to pair                    │    │
│  └─────────────────────────────────────────────────────┘    │
│                          ▲                                  │
│                          │                                  │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ 👤 TRUSTED USER                                     │    │
│  │    • Can read emails, search web                    │    │
│  │    • Can use approved tools                         │    │
│  │    • Cannot change settings                         │    │
│  │    • Paired via OTP by operator                     │    │
│  └─────────────────────────────────────────────────────┘    │
│                          ▲                                  │
│                          │                                  │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ 👥 GROUP MEMBER                                     │    │
│  │    • Read-only access                               │    │
│  │    • Can ask questions                              │    │
│  │    • Cannot trigger sensitive tools                 │    │
│  └─────────────────────────────────────────────────────┘    │
│                          ▲                                  │
│                          │                                  │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ ❓ UNKNOWN                                          │    │
│  │    • Can only request pairing                       │    │
│  │    • "Who are you? Get an OTP from the owner."      │    │
│  └─────────────────────────────────────────────────────┘    │
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
       ├─────────────────────────────▶│◀ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ │
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
┌────────────────────────────────────────────────────────────┐
│                        SKILLS                              │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  Trigger Types:                                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ /command    │ User types "/briefing"                │   │
│  │ schedule    │ Cron: "0 7 * * *" (daily at 7 AM)     │   │
│  │ event       │ "email.new" → when new email arrives  │   │
│  │ keywords    │ ["urgent", "asap"] in message         │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  Skill Types:                                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ workflow    │ Multi-step: fetch data → process →    │   │
│  │             │ summarize → send                      │   │
│  │ prompt      │ Inject context into AI conversation   │   │
│  │ reactive    │ Respond to specific events            │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  Example: Morning Briefing Skill                           │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ name: morning-briefing                              │   │
│  │ trigger:                                            │   │
│  │   schedule: "0 7 * * *"    # 7 AM daily             │   │
│  │   command: "/briefing"     # or manual trigger      │   │
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
│                                                            │
└────────────────────────────────────────────────────────────┘
```

### Web UI for Skills

Non-technical users can create skills through a web browser:

```
┌─────────────────────────────────────────────────────────────┐
│  Crayfish Skills                           [+ New Skill]    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ morning-briefing                         [workflow] │    │
│  │ Daily summary of emails and news                    │    │
│  │ Schedule: 0 7 * * *    Command: /briefing           │    │
│  │                                    [View] [Delete]  │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ urgent-alert                            [reactive]  │    │
│  │ Notify immediately on urgent emails                 │    │
│  │ Event: email.new    Keywords: urgent, asap          │    │
│  │                                    [View] [Delete]  │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

---

## Admin Dashboard

The dashboard is the control center — a tabbed single-page app served at `/` that replaces the old minimal "Crayfish is running" page. It's a single HTML file embedded in a Go const (same pattern as the skills UI), requiring no build tools.

```
┌──────────────────────────────────────────────────────────┐
│  Crayfish Dashboard                                       │
├──────────────────────────────────────────────────────────┤
│  [Overview] [Settings] [Skills] [Sessions] [Memory] [Events]
├──────────────────────────────────────────────────────────┤
│                                                          │
│  Overview:    Stats, adapters, uptime                    │
│  Settings:    Hot-reload name/personality/prompt;         │
│               restart-needed for provider/API key         │
│  Skills:      Create/edit/delete YAML workflows          │
│  Sessions:    Browse sessions, view message history      │
│  Memory:      FTS5 full-text search, delete entries      │
│  Events:      Filter by type, auto-refresh               │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

**Config hot-reload**: The `AppAccessor` interface bridges the gateway and app layers. Settings changes fall into two categories:

| Immediate (green dot)          | Restart needed (yellow dot)      |
|--------------------------------|----------------------------------|
| name, personality, system_prompt | api_key, provider, model        |
| continuity, session resume     | telegram_token, gmail settings   |
| snapshots, auto_update         | listen_addr, brave_api_key       |

The runtime's `UpdateConfig()` method applies identity changes immediately via a `sync.RWMutex`-protected config update. Config is persisted to YAML via `SaveConfig()`.

---

## Session Continuity

Crayfish preserves conversational context across summarization boundaries and session gaps.

```
┌────────────────────────────────────────────────────────────┐
│                   SESSION CONTINUITY                        │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  Problem: When conversation history is summarized to       │
│  save tokens, the "texture" of the conversation is lost    │
│  — active tasks, decisions in flight, conversational tone. │
│                                                            │
│  Solution: Session Snapshots                               │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ Snapshot captures:                                  │   │
│  │  • active_task — What was being worked on?          │   │
│  │  • last_exchanges — Recent conversational texture   │   │
│  │  • pending_proposals — Unresolved suggestions       │   │
│  │  • decisions_in_flight — Things being decided       │   │
│  │  • conversational_tone — Formal? Casual? Technical? │   │
│  │  • key_resources — Files, URLs, references          │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  Triggers:                                                 │
│  • Summarization compresses history → snapshot saved       │
│  • User resumes after idle gap → snapshot injected         │
│  • Manual checkpoint via tool                              │
│                                                            │
│  Lifecycle:                                                │
│  • Max snapshots per session (default: 3)                  │
│  • Old snapshots cleaned up hourly                         │
│  • Only the latest is marked "current"                     │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

---

## Identity System (SOUL.md + USER.md)

Crayfish maintains two markdown files that give the agent a real personality and knowledge of its owner. These files live alongside the config at `~/.config/crayfish/`.

```
┌────────────────────────────────────────────────────────────┐
│                   IDENTITY SYSTEM                           │
├────────────────────────────────────────────────────────────┤
│                                                            │
│  SOUL.md — Agent personality, values, tone                 │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ I'm warm and curious. I love creative projects.     │   │
│  │ I prefer casual language with a touch of humor.     │   │
│  │ I value privacy and simplicity above all else.      │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  USER.md — Info about the human                            │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ Name: Alice                                         │   │
│  │ Job: Software engineer at a startup                 │   │
│  │ Timezone: PST                                       │   │
│  │ Goals: Ship v2 of the product by March              │   │
│  │ Prefers: Brief, actionable responses                │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                            │
│  Properties:                                               │
│  • Max 4KB per file on disk                                │
│  • Cached in memory, truncated to ~2000 chars (~500 tok)   │
│  • Thread-safe (sync.RWMutex)                              │
│  • Valid UTF-8 enforced                                    │
│  • Missing files = empty strings (backward compatible)     │
│                                                            │
│  Tools:                                                    │
│  • identity_read  — Read SOUL.md or USER.md                │
│  • identity_update — Write new content to either file      │
│  • Both require Operator trust tier                        │
│                                                            │
│  First-Conversation Interview:                             │
│  When USER.md is empty, the system prompt includes an      │
│  interview instruction that guides the agent to naturally  │
│  learn about the user. After collecting enough facts, the  │
│  agent calls identity_update to save USER.md. Once saved,  │
│  the interview prompt stops appearing automatically.       │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

---

## MCP (Model Context Protocol)

MCP lets Crayfish connect to external tools without code changes.

```
┌─────────────────────────────────────────────────────────────┐
│                           MCP                               │
│              Connect to External Tool Servers               │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Crayfish ◀──────▶ MCP Server ◀──────▶ External Service     │
│                                                             │
│  Examples:                                                  │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ GitHub MCP      │ Create issues, PRs, read repos    │    │
│  │ Notion MCP      │ Search/create pages               │    │
│  │ Filesystem MCP  │ Read/write local files            │    │
│  │ Database MCP    │ Query databases                   │    │
│  │ Calendar MCP    │ Manage calendar events            │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  Transport:                                                 │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ stdio   │ Launch subprocess, communicate via stdin  │    │
│  │ HTTP    │ Connect to remote HTTP endpoint           │    │
│  └─────────────────────────────────────────────────────┘    │
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
│                           MESSAGE FLOW                                   │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  📱 You (Telegram)                                                       │
│      │                                                                   │
│      │ "What's in my inbox?"                                             │
│      ▼                                                                   │
│  ┌────────────────┐                                                      │
│  │ Telegram Bot   │ ◀── Telegram servers push update                     │
│  │ Adapter        │                                                      │
│  └───────┬────────┘                                                      │
│          │                                                               │
│          │ Publish: message.inbound                                      │
│          ▼                                                               │
│  ┌────────────────┐                                                      │
│  │  CrayfishBus   │ ◀── Event stored in SQLite                           │
│  │  (Event Log)   │                                                      │
│  └───────┬────────┘                                                      │
│          │                                                               │
│          │ Runtime subscribes to inbound events                          │
│          ▼                                                               │
│  ┌────────────────┐                                                      │
│  │    Runtime     │                                                      │
│  │                │                                                      │
│  │  1. Session?   │──▶ Look up Telegram user ID → Found: Operator        │
│  │                │                                                      │
│  │  2. Context    │──▶ Load identity (SOUL.md + USER.md) + memory         │
│  │                │    + interview prompt if USER.md empty                │
│  │                │                                                      │
│  │  3. LLM Call   │──▶ Send to Claude API                                │
│  │                │                                                      │
│  │  4. Response:  │◀── Claude says: "Use email_search tool"              │
│  │     Tool Call  │                                                      │
│  │                │                                                      │
│  │  5. Execute    │──▶ email_search({ limit: 10 })                       │
│  │     Tool       │◀── Returns: [email1, email2, ...]                    │
│  │                │                                                      │
│  │  6. LLM Call   │──▶ Send tool result to Claude                        │
│  │     #2         │◀── Claude: "You have 3 unread emails..."             │
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
│  │ Telegram Bot   │──▶ Send message via Telegram API                     │
│  │ Adapter        │                                                      │
│  └────────────────┘                                                      │
│          │                                                               │
│          ▼                                                               │
│  📱 You see: "You have 3 unread emails: ..."                             │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## Performance Budget

Crayfish is designed to run on minimal hardware:

```
┌─────────────────────────────────────────────────────────────┐
│                   PERFORMANCE TARGETS                       │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Memory:        < 128 MB RSS                                │
│  Binary Size:   < 20 MB                                     │
│  Cold Start:    < 3 seconds                                 │
│  Per Message:   < 50 KB overhead                            │
│  Database:      Compacted at 500 MB                         │
│                                                             │
│  Target Hardware:                                           │
│  ┌─────────────────────────────────────────────────────┐    │
│  │ Raspberry Pi 2/3/4/5                                │    │
│  │ 512 MB - 8 GB RAM                                   │    │
│  │ Any SD card (8 GB+)                                 │    │
│  │ WiFi or Ethernet                                    │    │
│  │ ~$35-75 total cost                                  │    │
│  └─────────────────────────────────────────────────────┘    │
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
│   ├── app/                  # Application orchestration & config
│   │   ├── app.go            # Component wiring, AppAccessor
│   │   └── config.go         # YAML/env config, SaveConfig
│   ├── bus/                  # Event bus (CrayfishBus)
│   ├── calendar/             # Google Calendar (CalDAV)
│   ├── channels/             # Channel adapters
│   │   ├── telegram/         # Telegram bot (long-polling)
│   │   └── cli/              # Terminal interface
│   ├── gateway/              # HTTP server & web UIs
│   │   ├── gateway.go        # Main orchestrator, AppAccessor interface
│   │   ├── dashboard_api.go  # REST API for dashboard
│   │   ├── dashboard_ui.go   # Admin dashboard SPA
│   │   ├── skills_api.go     # REST API for skills
│   │   └── skills_ui.go      # Skills management UI
│   ├── gmail/                # Gmail IMAP integration
│   ├── heartbeat/            # Proactive check-ins
│   ├── identity/             # Identity files (SOUL.md + USER.md)
│   ├── mcp/                  # MCP client
│   ├── provider/             # LLM providers (Anthropic, OpenAI, etc.)
│   ├── runtime/              # Agent brain, tool loop, memory, snapshots
│   ├── security/             # Sessions, pairing, trust, guardrails
│   ├── setup/                # First-time web wizard
│   ├── skills/               # Skills system (YAML workflows)
│   ├── storage/              # SQLite wrapper & migrations
│   ├── tools/                # Built-in tool registry
│   ├── updater/              # Auto-update system
│   └── voice/                # STT (whisper.cpp) & TTS (Piper)
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

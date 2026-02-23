# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Crayfish

Crayfish is a personal AI assistant built as a single Go binary, designed to run on hardware from a Raspberry Pi Zero ($5) to modern workstations. It prioritizes accessibility, privacy (all data stays local), and simplicity — no Docker, no microservices, no cloud dependencies.

## Build & Development Commands

```bash
make build          # Build for current platform (CGO_ENABLED=0, static binary)
make run            # Build and run locally
make test           # Run tests: go test -v -race -count=1 ./...
make lint           # go vet + staticcheck
make fmt            # gofmt -s -w .
make deploy         # Build + SSH push + restart on Raspberry Pi
make build-all      # Cross-compile all 6 targets (linux arm/amd64, darwin amd64/arm64)
make check-size     # Verify binary stays under 20MB limit
```

Run a single test file or package:
```bash
go test -v -race ./internal/bus/
go test -v -race -run TestSpecificName ./internal/storage/
```

## Architecture

Three-layer design, all wired together in `internal/app/app.go`:

**Gateway** (`internal/gateway/`) — HTTP server on `:8119`. Manages channel adapters (Telegram, CLI) and routes messages through the event bus. Serves the web setup wizard and skills API.

**Runtime** (`internal/runtime/`) — The agent brain. Consumes events from the bus, assembles context (session → memory → history), calls the LLM, executes tools in a loop (max 10 iterations per message), and returns responses. Memory extraction and retrieval happen here.

**Storage** (`internal/storage/`) — Single SQLite file (WAL mode, pure Go driver, no CGo). Tables: events, sessions, memory_fts (FTS5), memory_metadata, messages, skills.

### Key subsystems

- **Bus** (`internal/bus/`) — SQLite-backed append-only event log. Event types: `message.inbound`, `message.outbound`, `tool.request`, `tool.result`, `system.*`. Enables crash recovery via replay.
- **Provider** (`internal/provider/`) — LLM provider factory. Auto-detects provider from model name. Supports Anthropic, OpenAI, Groq, DeepSeek, Together, OpenRouter, Ollama, vLLM, LM Studio. Non-Anthropic providers use an OpenAI-compatible API path.
- **Security** (`internal/security/`) — Four trust tiers: Unknown → Group → Trusted → Operator. OTP-based device pairing. Tool access is gated by tier.
- **Tools** (`internal/tools/`) — Tool registry with built-in tools (memory, email, web search, MCP). Each tool declares its minimum trust tier.
- **Skills** (`internal/skills/`) — YAML-defined workflows (prompt, workflow, reactive types). Triggers: commands, cron schedules, events, keywords. No shell execution — steps invoke registered tools only.
- **Channels** (`internal/channels/`) — Input/output adapters. Telegram (HTTP long-polling) and CLI implementations.
- **Setup** (`internal/setup/`) — Web-based first-time configuration wizard with hardware detection and model recommendations.

### Startup flow

`cmd/crayfish/main.go` → loads config → if no API key, launches setup wizard → `app.New()` → `app.Start()` wires: storage → bus → sessions → LLM provider → tools → skills → channels → gateway → runtime → heartbeat/updater.

## Configuration

Priority: environment variables (`CRAYFISH_*`) > YAML config file > defaults. Config paths searched: `./crayfish.yaml`, `~/.config/crayfish/crayfish.yaml`, `/etc/crayfish/crayfish.yaml`.

## Constraints

- **3 direct dependencies only**: go-imap, yaml.v3, modernc.org/sqlite. Keep it minimal.
- **No CGo**: All builds use `CGO_ENABLED=0` for cross-compilation.
- **Binary size < 20MB**: Enforced by `make check-size`.
- **Single SQLite file**: No external databases or message queues.

## Git Commits

- Do NOT add "Co-Authored-By" lines to commit messages.

## Workflow Orchestration

### 1. Plan Node Default
- Enter plan mode for ANY non-trivial task (3+ steps or architectural decisions)
- If something goes sideways, STOP and re-plan immediately - don't keep pushing
- Use plan mode for verification steps, not just building
- Write detailed specs upfront to reduce ambiguity

### 2. Subagent Strategy
- Use subagents liberally to keep main context window clean
- Offload research, exploration, and parallel analysis to subagents
- For complex problems, throw more compute at it via subagents
- One task per subagent for focused execution

### 3. Self-Improvement Loop
- After ANY correction from the user: update 'tasks/lessons.md' with the pattern
- Write rules for yourself tht prevent the same mistake
- Ruthlessly iterate on these lessons until mistake rate drops
- REview lessons at session start for relevant project

### 4. Verification Before Done
- Never mark a task complete without proving it works
- Diff behavior between main and your changes when relevant
- Ask yourself: "Would a staff engineer aprove this?"
- Run tests, check logs, demonstrate correctness

### 5. Demand Elegance (Balanced)
- For non-trivial changes,: pause and ask "is there a more elegant way?"
- If a fix feels hacky: "Knowing everything I know, implement the elegant solution"
- Skip this for simple, obvious fixes - don't over-engineer
- challenge your own work before presenting it

### 6. Autonomous Bug Fixing
- When given a bug report: just fix it. Don't ask for hand-holding
- Point at logs, errors, failing tests - then resolve them
- Zero context switching required from the user
- Go fix failing CI tests without being told how

## Task Management
1. **Plan First**: Write plan to 'tasks/todo.md' with checkable items
2. **Verify Plan**: Check in before starting implementation
3. **Track Progress**: Mark items complete as you go
4. **Explain Changes**: High-level summary at each step
5. **Document Results**: Add review section to 'tasks/todo.md'
6. **Capture Lessons**: Update 'tasks/lessons.md' after corrections

## Core Principles
- **Simplicity First**" Make every change as simple as possible
- **No Laziness**: Find root causes. No temporary fixes. Senior DEveloper Standards
- **Minimal Impact**: Changes should only touch what's necessary. Avoid introducing bugs.
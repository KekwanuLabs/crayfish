# Network Architecture

This document describes how Crayfish connects to the network — what ports it
uses, what external services it calls, what data leaves your device, and how
the firewall and Cloudflare Tunnel protect it.

---

## Design Principles

1. **All connections are outbound.** Nothing on the internet can directly reach
   your Pi. The firewall blocks all inbound connections from external sources.
2. **LAN access only for the UI.** The dashboard and API (port 8119) are
   accessible only from your local network.
3. **Cloudflare Tunnel for phone calls.** When Twilio ConversationRelay needs
   to reach Crayfish, it does so through an outbound Cloudflare Tunnel — no
   open inbound port required.
4. **Data stays on your device.** Conversation history, memory, todos, email
   cache, session data — all stored locally in SQLite. Only the minimum
   necessary data is sent to each external service.

---

## Network Topology

```
╔══════════════════════════════════════════════════════════════╗
║                     YOUR LOCAL NETWORK                       ║
║                                                              ║
║  Your phone / laptop ──────► Crayfish :8119 (dashboard/API) ║
║  Your phone / laptop ──────► Crayfish :22   (SSH)           ║
║                                                              ║
║  Firewall: both ports restricted to local subnets only.      ║
║  Rules update automatically as network interfaces change.    ║
╚══════════════════════════════════════════════════════════════╝
                    │
                    │ Outbound only (firewall blocks all inbound)
                    │ Cloudflare Tunnel keeps one persistent
                    │ outbound connection to Cloudflare's edge.
                    ▼
    ┌───────────────────────────────────────────────┐
    │           CLOUDFLARE EDGE (optional)          │
    │                                               │
    │  Twilio phone calls arrive here               │
    │  ↓ Forwarded to Crayfish via tunnel           │
    │  ↓ HMAC-SHA1 signature validated              │
    │  ↓ Only /phone/twiml and /phone/ws exposed    │
    └───────────────────────────────────────────────┘
                    │
                    │ Outbound HTTPS/WSS only
                    ▼
    ┌─────────────────────────────────────────────────────────┐
    │             EXTERNAL SERVICES                           │
    │                                                         │
    │  LLM ──────► Anthropic / Groq / OpenAI / Ollama        │
    │  STT ──────► Groq Whisper  OR  local whisper.cpp        │
    │  TTS ──────► ElevenLabs    OR  local Piper              │
    │  Phone ────► Twilio ConversationRelay                   │
    │  Search ───► Brave Search API                           │
    │  Email ────► Gmail (OAuth) / IMAP server                │
    │  Calendar ─► Google Calendar API                        │
    │  Drive ────► Google Drive API                           │
    └─────────────────────────────────────────────────────────┘
```

---

## Port Reference

| Port | Protocol | Accessible from | Purpose |
|------|----------|-----------------|---------|
| 8119 | HTTP/WS  | Local network only | Dashboard, API, setup wizard, phone WebSocket |
| 22   | TCP      | Local network only | SSH management |

**No other ports are opened.** The firewall (`ufw`) default policy is
`deny incoming` — everything not explicitly listed above is blocked.

### What about the Cloudflare Tunnel?

The tunnel uses a **single outbound connection** on port 443 (HTTPS) or 7844
(QUIC) from the Pi to Cloudflare's servers. No inbound port is required.
Cloudflare multiplexes all tunnel traffic over this one outbound connection.

This means port 8119 never needs to be reachable from the internet — Twilio
reaches it through Cloudflare's infrastructure, not directly.

---

## Firewall

Crayfish manages `ufw` (Uncomplicated Firewall) dynamically. Rules are set
at install time and updated automatically by the firewall manager goroutine
every 3 minutes, and also by `ExecStartPre` in the systemd service before
Crayfish starts.

### Default policies

```
Default: deny incoming
Default: allow outgoing
```

### Managed allow rules (updated dynamically)

```
ALLOW from <detected-subnet> to any port 22 (SSH)
ALLOW from <detected-subnet> to any port 8119 (dashboard)
```

Subnets are detected from all active network interfaces using Go's
`net.Interfaces()`. Both IPv4 and safe IPv6 ranges are detected:

- **IPv4:** any non-link-local address (not 169.254.x.x)
- **IPv6 link-local** (`fe80::/10`): allowed — not routable beyond the link
- **IPv6 ULA** (`fc00::/7`): allowed — IPv6 private space, equivalent to RFC 1918
- **IPv6 global unicast**: blocked — internet-routable, treated as external

### What happens when the Pi moves to a new network?

1. `ExecStartPre` (`/usr/local/bin/crayfish-firewall-sync`) runs before
   Crayfish starts — detects current subnets and updates ufw immediately.
2. The firewall manager goroutine also syncs within 3 minutes of any change.
3. Old subnet rules are removed; new ones are added. Only Crayfish-managed
   rules for ports 22 and 8119 are touched — any manually added rules are
   left intact.

---

## Cloudflare Tunnel

### Quick Tunnel (default, automatic)

When Twilio is configured, Crayfish automatically starts a Cloudflare quick
tunnel using `cloudflared`. No account required.

- URL format: `https://random-words.trycloudflare.com`
- URL changes on restart
- Crayfish detects the new URL and updates the Twilio webhook automatically
- Webhook update retries up to 10 times with exponential backoff
- Failure is reported via Telegram

### Named Tunnel (optional, stable URL)

Power users can configure a stable URL that never changes:

```bash
cloudflared tunnel login                      # OAuth with Cloudflare
cloudflared tunnel create crayfish            # creates stable tunnel
cloudflared tunnel route dns crayfish calls.yourdomain.com
```

Set `CRAYFISH_TUNNEL_URL=https://calls.yourdomain.com` or enter the URL
in Settings → Phone & Tunnel → Stable Tunnel URL.

### What is exposed through the tunnel?

Only two endpoints:

| Endpoint | Who can call it | Validation |
|----------|----------------|------------|
| `GET /phone/twiml` | Twilio only | HMAC-SHA1 signature (`X-Twilio-Signature`) |
| `WS /phone/ws` | Twilio only | HMAC-SHA1 signature before upgrade |

Any request without a valid Twilio signature is rejected with HTTP 403.

---

## External Services

All external connections use HTTPS (TLS 1.2+) or WSS. Here is every service
Crayfish may call, what it sends, and why:

### LLM (Language Model)

| Provider | Endpoint | Data sent | Required |
|----------|----------|-----------|---------|
| Anthropic | `api.anthropic.com` | Message history, system prompt | Yes (default) |
| Groq | `api.groq.com` | Message history, system prompt | If using Groq |
| OpenAI | `api.openai.com` | Message history, system prompt | If using OpenAI |
| Ollama | `localhost:11434` | Message history, system prompt | Local — no data leaves |

Message history includes only the current conversation context window
(recent messages + summarised earlier messages). Full history is stored
locally in SQLite.

### Speech-to-Text (STT)

| Provider | Endpoint | Data sent | Notes |
|----------|----------|-----------|-------|
| Groq | `api.groq.com/openai/v1/audio/transcriptions` | Audio clip | Voice messages only |
| OpenAI | `api.openai.com/v1/audio/transcriptions` | Audio clip | Fallback |
| whisper.cpp | Local process | Audio clip | Fully local, no data leaves |

Audio is sent only when a voice message is received. Text messages bypass STT.

### Text-to-Speech (TTS)

| Provider | Endpoint | Data sent | Notes |
|----------|----------|-----------|-------|
| ElevenLabs | `api.elevenlabs.io/v1/text-to-speech` | Response text | Used for voice replies |
| Piper | Local process | Response text | Fully local, no data leaves |

Only responses that are under the configured character limit (default: 500)
are synthesised. Longer responses are sent as text.

### Phone Calls

| Service | Endpoint | Data sent | Notes |
|---------|----------|-----------|-------|
| Twilio REST API | `api.twilio.com` | Call metadata (numbers, TwiML URL) | To initiate calls |
| Twilio ConversationRelay | Inbound via Cloudflare Tunnel | Nothing outbound | Call audio handled by Twilio |

Crayfish never transmits audio directly to Twilio. Call audio stays within
Twilio's infrastructure. Crayfish exchanges only text (transcribed utterances
and text responses) over the ConversationRelay WebSocket.

### Email

| Service | Endpoint | Data sent | Notes |
|---------|----------|-----------|-------|
| Gmail (OAuth) | `mail.google.com` IMAP + Google APIs | OAuth token for auth | Read + limited write |
| IMAP server | Your IMAP host, port 993 | OAuth or app password | Read + limited write |

Email content is fetched and cached locally in SQLite. Crayfish does not
upload email content to any service other than your own mail provider.

### Web Search

| Service | Endpoint | Data sent |
|---------|----------|-----------|
| Brave Search | `api.search.brave.com` | Search query string |

### Model Download (one-time)

| Service | Endpoint | Data sent | When |
|---------|----------|-----------|------|
| HuggingFace | `huggingface.co` | None | Piper voice model download (once) |
| GitHub Releases | `github.com` | None | Piper binary download (once) |

---

## SSH Access

### From local network (always works)

SSH is accessible from any device on the same local network as the Pi.
The firewall allows SSH from all detected local subnets.

```bash
ssh crayfish@192.168.1.234  # or whatever your Pi's IP is
```

### From external networks (requires additional setup)

Port 22 is blocked from the internet. For remote SSH, use one of:

**Option 1 — Cloudflare Tunnel SSH (recommended)**

```bash
# On Pi: create SSH route for named tunnel
cloudflared tunnel route dns crayfish ssh.yourdomain.com

# From anywhere with cloudflared installed:
ssh -o ProxyCommand="cloudflared access ssh --hostname ssh.yourdomain.com" \
    crayfish@ssh.yourdomain.com
```

**Option 2 — Tailscale (simplest)**

```bash
# On Pi:
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up

# From anywhere with Tailscale:
ssh crayfish@$(tailscale ip -4)
```

### SSH hardening

Disable password authentication (key-only) to eliminate the brute-force
attack surface entirely:

```bash
sudo sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config
sudo sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
sudo systemctl restart sshd
```

Ensure your SSH public key is in `~/.ssh/authorized_keys` before running this.

---

## Security Summary

| Surface | Protection |
|---------|------------|
| Internet → Pi (direct) | Blocked by ufw (`deny incoming`) |
| Internet → Dashboard | Blocked by ufw + `requireAuth` middleware |
| Internet → SSH | Blocked by ufw (LAN-only rule) |
| Internet → Phone endpoints | Cloudflare Tunnel + HMAC-SHA1 Twilio signature |
| LAN → Dashboard | Bearer token required for non-local IPs |
| LAN → SSH | Open (by design — required for management) |
| Data at rest | SQLite on-device, no cloud backup |
| Data in transit | HTTPS/TLS for all external connections |
| API keys | Stored in `crayfish.yaml` (mode 0600), never logged |

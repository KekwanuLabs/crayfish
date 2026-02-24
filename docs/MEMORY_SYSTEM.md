# Crayfish Persistent Memory System

## Overview

The persistent memory system ensures Crayfish never forgets important context, preferences, and decisions from conversations. It automatically extracts and stores memorable facts that persist across sessions, independent of conversation summarization.

## Architecture

### Components

1. **MemoryExtractor** (`internal/runtime/memory_extractor.go`)
   - Automatically extracts important facts from conversation turns using the LLM
   - Categorizes facts (preference, personal, decision, context, general)
   - Assigns importance scores (1-10)
   - Debounced to run at most once per 5 seconds per session
   - Runs asynchronously (non-blocking)

2. **MemoryRetriever** (`internal/runtime/memory_retriever.go`)
   - Retrieves relevant memories using FTS5 search
   - Ranks by FTS5 relevance and importance score
   - Tracks access patterns (last_accessed, access_count)
   - Formats memories into readable context for the LLM

3. **Memory Tools** (`internal/tools/tools.go`)
   - `memory_save` - Manually save a memory with category and importance
   - `memory_search` - Search memories with optional category filter
   - `memory_list` - List all memories with filtering
   - `memory_delete` - Delete a specific memory
   - `memory_update` - Update memory content or metadata
   - `memory_stats` - Show memory usage statistics

### Database Schema

**memory_fts** (FTS5 virtual table)
- `key` - Short title/identifier
- `content` - Full memory content
- `session_id` - User session (privacy isolation)
- `created_at` - Timestamp

**memory_metadata** (new table added in migration #4)
- `id` - Links to memory_fts via rowid
- `session_id` - Session identifier
- `category` - One of: preference, personal, decision, context, general
- `importance` - Score 1-10
- `source_context` - How this memory was created
- `created_at` - Creation timestamp
- `last_accessed` - Last retrieval timestamp
- `access_count` - Number of times retrieved

## How It Works

### Automatic Extraction

1. After each conversation turn, `MemoryExtractor.ExtractFromTurn()` is called asynchronously
2. The LLM analyzes the user message and assistant response
3. Extracts 0-3 significant facts with category, importance, and content
4. Stores facts in both `memory_fts` and `memory_metadata`
5. Debounced to prevent extraction spam

**Extraction Criteria:**
- Messages must be > 20 characters
- Skips trivial greetings/confirmations
- Only extracts information likely useful in future conversations
- Focuses on specific, actionable facts

### Automatic Retrieval

1. During context assembly (`assembleContext` in runtime.go)
2. Uses the current user message as the FTS5 search query
3. Retrieves top 5 most relevant memories for the session
4. Formats them as a system message injected before conversation history
5. Updates access tracking metadata

**Retrieval Query:**
```sql
SELECT mf.key, mf.content, mm.category, mm.importance, mf.created_at
FROM memory_fts mf
JOIN memory_metadata mm ON mf.rowid = mm.id
WHERE memory_fts MATCH ? AND mm.session_id = ?
ORDER BY rank, mm.importance DESC
LIMIT 5
```

### Context Format

Retrieved memories are formatted as:
```
[Relevant memories from past conversations]

Preferences:
- User prefers Python for scripting

Personal Context:
- User is building an AI agent framework called Crayfish

Recent Decisions:
- Decided to use SQLite over PostgreSQL for storage
```

## Privacy & Security

- **Session Isolation**: All memories are scoped to `session_id`
- **Trust Tiers**: Memory tools require TierOperator or TierTrusted
- **No Cross-User Learning**: Each user's memories are completely isolated

## Performance

- **Extraction**: < 5 seconds (runs async, non-blocking)
- **Retrieval**: < 100ms (FTS5 indexed search)
- **Storage**: ~1KB per memory fact
- **Debouncing**: Max 1 extraction per 5 seconds per session

## Integration Points

### Runtime Integration
- `runtime.go`: MemoryExtractor and MemoryRetriever fields in Runtime struct
- `runtime.go`: `New()` constructor accepts memory components
- `runtime.go`: `handleInbound()` triggers extraction after response
- `runtime.go`: `assembleContext()` injects memories during context assembly

### App Initialization
- `app.go`: `Start()` initializes memory components
- `app.go`: Passes to Runtime constructor

### Tool System
- `tools.go`: Tool.Execute accepts `*security.Session` parameter
- All tool Execute functions accept `*security.Session`

## Usage Examples

### Automatic Extraction
```
User: I prefer Python over JavaScript for scripting
Crayfish: I'll remember that!
[Automatically extracts: category=preference, importance=7,
 key="Python preference", content="User prefers Python over JavaScript for scripting"]
```

### Manual Memory Save
```
memory_save(
  key="Project tech stack",
  content="Using Go, SQLite, and Anthropic Claude for Crayfish",
  category="decision",
  importance=8
)
```

### Memory Search
```
memory_search(query="programming language", category="preference")
// Returns: [preference|7] Python preference: User prefers Python over JavaScript for scripting
```

### Memory Statistics
```
memory_stats()
// Returns:
// Memory Statistics:
// Total memories: 23
//
// By category:
//   preference: 8
//   personal: 5
//   decision: 6
//   context: 3
//   general: 1
//
// Most accessed: Python preference (12 times)
```

## Testing

Run memory system tests:
```bash
go test ./internal/runtime -v -run TestMemory
```

## Future Enhancements

1. **Memory consolidation** - Merge duplicate/redundant memories
2. **Memory decay** - Age out old, unused memories
3. **Cross-session patterns** - (Privacy-preserving) general knowledge extraction
4. **Memory importance tuning** - Learn from access patterns
5. **Semantic search** - Beyond FTS5 keyword matching

## Migration Path

The system is backwards compatible:
- Existing `memory_fts` entries work as-is
- New `memory_metadata` table links via rowid
- Old manual saves (without metadata) still searchable
- Automatic extraction is opt-in (enabled by default)

# Lessons Learned

## 2026-02-27: Tool loop max-iteration boundary bug

**Bug**: When the agentic tool loop hits its last iteration (iteration 9 of 10), the LLM returns text + tool calls in the same turn. Tools execute, but the loop exits using the LLM's *pre-execution* text as the final response. The LLM never sees tool results, so it says "I've completed the requested actions" without acknowledging what actually happened.

**Root cause**: `runtime.go` line 550-556 — the max iteration boundary check set `finalContent = resp.Content` *after* executing tools but *without* giving the LLM a final call to see the results.

**Fix**: On the last iteration, after tool execution, make one additional LLM call with the tool results in context so it can compose a proper response.

**Pattern to watch for**: Any loop boundary that terminates without letting the LLM see the results of the last batch of tool executions.

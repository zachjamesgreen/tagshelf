# tagshelf

## Agent skills

### Issue tracker

Issues, specs and wayfinder maps live in the Obsidian vault `Notes` under `tagshelf/`, accessed via the `obsidian` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: `CONTEXT.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

### Code navigation

Serena is configured in `.mcp.json`. At session start, call its `initial_instructions` tool. Prefer Serena's symbol tools (find symbol, find referencing symbols, symbols overview, replace symbol body, rename) over Grep/Read/Edit for navigating and editing Go code; fall back to text tools for non-code files or plain-text searches.

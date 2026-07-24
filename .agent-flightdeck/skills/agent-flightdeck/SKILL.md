# Agent Flightdeck

Use this shared project skill from Claude Code, Codex, Cursor, or any other coding agent when a session should inspect, retrieve, or apply Agent Flightdeck controls.

## Workflow

1. Run `cockpit systems` to inspect configured coding agents, MCP servers, skills, shared skills, and graphify state.
2. Run `cockpit status` or `cockpit plan` when context, budget, or route drift needs attention.
3. Run `cockpit memory --json` when a downstream system needs compact session memory.
4. Run `cockpit checklist <topic>` for focused procedures such as context, budget, search, or faults.
5. Run `cockpit list` to inspect pending suggestions.
6. Use `cockpit apply <n> --dry-run` before accepting a suggested instruction, MCP, or skill change.

## Boundaries

- Treat cockpit suggestions as advisory.
- Prefer dry runs before project writes.
- Keep accepted controls in this shared skill so Claude Code, Codex, and Cursor read the same guidance.

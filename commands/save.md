---
description: Save a memory to persistent cross-tool storage. Pass content as argument.
argument-hint: <content>
---

Save the following to Anchored memory: $ARGUMENTS

If the argument is empty, ask the user what to save.

Then call the `anchored_save` MCP tool. You MUST pick the best category from: `fact`, `preference`, `decision`, `event`, `learning`, `plan`, `summary`. Picking explicitly beats letting the regex auto-detect. Confirm to the user which category you chose and why, plus the project it was scoped to.

## Remote Save

When working in a project that has a remote server configured, you SHOULD also save to the remote by passing `--remote`. This makes the memory available to team members and other machines.

- `--remote` (no value): save to the default remote server
- `--remote=name`: save to a named remote server

The save always succeeds locally first. If the remote save fails (403, network error), the local save is preserved and a warning is printed — the flow never breaks.

Categories `event` and `preference` are blocked from remote sync. Use `fact`, `decision`, `plan`, `summary`, or `learning` for team-shareable memories.

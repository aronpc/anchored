---
description: Save a memory to persistent cross-tool storage. Pass content as argument.
argument-hint: <content>
---

Save the following to Anchored memory: $ARGUMENTS

If the argument is empty, ask the user what to save.

Then call the `anchored_save` MCP tool. You MUST pick the best category from: `fact`, `preference`, `decision`, `event`, `learning`, `plan`, `summary`. Picking explicitly beats letting the regex auto-detect. Confirm to the user which category you chose and why, plus the project it was scoped to.

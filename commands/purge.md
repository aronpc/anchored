---
description: Wipe Anchored memory (default soft-delete; pass --hard for full DB reset with backup)
argument-hint: [--hard]
---

The user wants to purge Anchored memory. Arguments: $ARGUMENTS

This is destructive. Before running anything:

1. Confirm the user really wants to do this. Ask once, plainly.
2. If `$ARGUMENTS` contains `--hard`, warn them this drops the full DB (a `.bak` is made automatically). Otherwise it soft-deletes everything (recoverable for 30 days via `anchored dream`).
3. Only after explicit confirmation, run the matching command:
   - Soft purge: `anchored purge --yes`
   - Hard purge: `anchored purge --hard --yes`
4. Show the resulting count or backup path.

Never run `--hard` without an explicit "yes, hard reset" from the user.

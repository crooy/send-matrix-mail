# ADR-0002: Default Room Delivery with Mentions

**Status:** Accepted  
**Date:** 2026-07-29

## Context

ADR-0001 defined a three-tier resolution (DM → room → default). In practice, DM
room creation caused problems:

- Conduit creates phantom DM rooms for non-existent users, requiring an extra
  profile lookup per recipient.
- Even for real users, every addressed recipient gets a new DM room, cluttering
  the user's room list.
- System monitoring tools (rkhunter, fail2ban, unattended-upgrades) send to a
  fixed address — the operator wants all notifications in one room.

Additionally, the `Send` method only included the default room when there were
no recipients, making it useless as a fallback for addressed messages.

## Decision

All messages are delivered to the configured `default_room`. Recipients are
mentioned via `m.mentions` (Matrix push notifications) instead of getting their
own DM room. Room-alias recipients (`server-bots@host` → `#server-bots:host`)
still resolve to their specific room if the alias exists — the DM step is simply
removed.

New resolution algorithm for each recipient:

1. **Named room:** Construct `#localpart:domain`. Attempt to join the room. If
   the room exists and is joinable → deliver to that room.
2. **Default room:** Always deliver to the configured `default_room`.
3. **Mentions:** Recipients matching `user@domain` are collected and sent as
   `m.mentions.user_ids` in the message event.

### Comparison to ADR-0001

| Aspect | ADR-0001 (old) | ADR-0002 (new) |
|---|---|---|
| DM rooms | Created per recipient | Never created |
| Default room | Only when no recipients | Always |
| User notifications | Via DM room | Via `m.mentions` |
| Room-alias targeting | Yes (step 2) | Yes (now step 1) |
| Latency | Up to 2 round-trips/recipient | Up to 1 round-trip/recipient |

## Consequences

- **Positive:** No phantom DM rooms — Conduit can't create rooms for users who
  don't exist because we never call `/createRoom` for DMs.
- **Positive:** All system notifications land in one room. Users get push
  notifications via `m.mentions` instead of room-level noise.
- **Positive:** Lower latency — one fewer API call per recipient.
- **Negative:** Loss of per-recipient room isolation. Two messages to different
  users appear in the same room. Acceptable for this use case (system
  monitoring), and mention highlighting distinguishes recipients.
- **Negative:** `m.mentions` requires Matrix v1.4+ — older clients may not show
  mentions. The user ID is also included in the message body as a fallback.
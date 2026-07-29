# ADR-0001: Recipient Resolution Model

**Status:** Accepted  
**Date:** 2026-07-29

## Context

`send-matrix-mail` accepts recipients in standard sendmail style: positional arguments (`send-matrix-mail user@host`) and/or extracted from `To`/`Cc`/`Bcc` headers (via `-t`). But Matrix doesn't use email addresses — it uses room IDs, room aliases, and user IDs.

We need a rule for what `localpart@domain` means and where the message goes.

## Decision

Three-tier resolution, attempted in order for each recipient:

1. **Direct message:** Construct `@localpart:domain`. Create a DM room with that Matrix user. If the user exists and accepts DMs → deliver there.
2. **Named room:** Construct `#localpart:domain`. Attempt to join the room. If the room exists and is joinable → deliver there.
3. **Default room:** Fall back to the configured `default_room`.

**Deduplication:** After resolving all recipients, collect unique room IDs. Deliver one copy per unique room. If the default room was already targeted explicitly (e.g., `room1@host` resolved to the default room), do not add a duplicate.

**Partial resolution:** If some recipients resolve and others fail permanently (e.g. user not found, room not joinable), the resolved targets still receive the message. Only if *all* recipients fail permanently does the invocation exit non-zero without delivery.

**Transient failures during resolution:** If resolution fails transiently (network error), the whole message is enqueued for retry — no partial delivery.

## Alternatives Considered

### A: Always default room (ignore recipients entirely)

Simplest. Every message goes to one configured room. Recipients are purely informational (shown in the message body).

**Rejected:** Wastes the sendmail recipient model. Users expect `send-matrix-mail alice@host bob@host` to differentiate delivery. Also makes the matrix channel a noisy firehose of all system mail.

### B: Recipient is a room alias, period

`localpart@domain` always maps to `#localpart:domain`. No DM support, no default room fallback.

**Rejected:** Can't DM individual users. Fails hard when a room doesn't exist — no graceful degradation.

### C: Config-based mapping table

A map in the config file: `{ "alerts": "#alerts:example.com", "oncall": "@oncall:example.com" }`. Recipients are keys in this table.

**Rejected:** Adds configuration burden. Every new recipient requires a config change. Breaks the "drop-in sendmail" experience where you can just type an address.

## Consequences

- **Positive:** Recipient addresses are self-describing. `alerts@example.com` naturally targets `#alerts:example.com`. No config needed per-recipient.
- **Positive:** DM support gives an immediate "notify a person" path without room setup.
- **Positive:** Default room fallback means messages never get lost — even a typo'd recipient still delivers.
- **Negative:** Resolution requires up to two network round-trips (DM attempt → room join attempt) per unique `localpart@domain`. This is sequential and could add latency for many recipients.
- **Negative:** The DM resolution step means we might create DM rooms with users who don't expect them. Acceptable because (a) the bot must be configured with credentials, (b) the sender explicitly addressed that recipient, and (c) the message body identifies the original author.

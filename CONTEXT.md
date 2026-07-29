# CONTEXT — send-matrix-mail

Domain glossary. No implementation details.

## Glossary

**Message**
The RFC 5322 document read from stdin: headers (From, To, Cc, Bcc, Subject, Date) followed by a blank line and a body. The unit of work the system delivers.

**Envelope**
The routing information that accompanies a message but is not part of the RFC 5322 body. Contains an author address (from `-f` flag, `From:` header, or environment) and one or more recipient addresses (from argv and/or `-t` header extraction). Analogous to SMTP's MAIL FROM / RCPT TO.

**Author**
The original human sender of the email — the "who wrote this." Resolved from `-f <addr>` → `From:` header → `$EMAIL` → `$USER`/`$LOGNAME`@domain → config default. Displayed in the Matrix message body, but does **not** authenticate to the homeserver.

**Recipient**
An address string of the form `localpart@domain`, collected from positional CLI arguments or from `To:`/`Cc:`/`Bcc:` headers (when `-t` is given). Each recipient undergoes resolution to determine its delivery target.

**Delivery Target**
The concrete Matrix destination for a message, after recipient resolution. One of:
- A **direct message** room with a Matrix user (`@localpart:domain`)
- A **named room** (`#localpart:domain`) the bot can access
- The **default room** (configured fallback)

**Bot Identity**
The Matrix user account that the tool authenticates as. This identity performs all Matrix API calls (login, join, send). Distinct from the Author — the bot is the transport, the author is the content.

**Spool**
The local filesystem queue that holds messages when the Matrix homeserver is unreachable. Messages in the spool are **pending** (awaiting next retry) or **failed** (permanently undeliverable). The spool is crash-safe via atomic `tmp/` → `queue/` → `failed/` directory transitions.

**Resolution**
The process of mapping each recipient address to one or more delivery targets. Algorithm:
1. Try to create a DM with `@localpart:domain`. If the user exists → deliver to that DM.
2. Try to join `#localpart:domain`. If the room exists and is joinable → deliver to that room.
3. Otherwise → deliver to the configured default room.

**Deduplication**
When multiple recipients resolve to the same delivery target (e.g. `alice@homeserver` and `bob@homeserver` both fall back to the default room), only one copy of the message is sent to that target. The default room never receives a duplicate if it was already targeted explicitly.

## Invariants

- A message is delivered **at most once** per delivery target per invocation.
- The bot identity must be able to join rooms and send `m.text` events.
- The author address is informational only — it appears in the message body, not in Matrix authentication.
- Permanent delivery failures (no such user, no such room, auth rejected) are **not** queued — they exit with a non-zero sysexits code immediately.
- Transient failures (network unreachable, homeserver 5xx, rate-limited) are enqueued and retried with exponential backoff.

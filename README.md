# send-matrix-mail

A drop-in `sendmail` replacement that delivers RFC 5322 mail as Matrix `m.text` messages.
Designed for cron, monitoring systems, and scripts that already pipe email through sendmail.

## Quick start

```sh
# Install
go install send-matrix-mail@latest

# Configure
mkdir -p ~/.config/send-matrix-mail
cat > ~/.config/send-matrix-mail/sendmailrc.toml << 'EOF'
[matrix]
homeserver = "https://matrix.example.com"
user_id    = "@bot:example.com"
password   = "your_password"
EOF

# Send a message
echo "Subject: disk full
From: cron@server
To: ops@matrix

Disk usage at 95% on /dev/sda1" | send-matrix-mail -t
```

## Installation

### From source (Go ≥1.22)

```sh
go install github.com/yourorg/send-matrix-mail@latest  # or
git clone https://github.com/yourorg/send-matrix-mail.git
cd send-matrix-mail
go build -o /usr/local/bin/send-matrix-mail .
```

Single static binary — zero runtime dependencies.

### From a release

Download the pre-built binary for your platform, place it in `$PATH`, and `chmod +x`.

## Configuration

Search order (first found wins):

1. `-C /path/to/config` (CLI flag, sendmail `-C` compat)
2. `$XDG_CONFIG_HOME/send-matrix-mail/sendmailrc.toml`
3. `~/.config/send-matrix-mail/sendmailrc.toml`
4. `/etc/send-matrix-mail/sendmailrc.toml`

### Full example

See [`send-matrix-mail.toml.example`](./send-matrix-mail.toml.example) for all options.

```toml
# State/spool directory (default: $XDG_STATE_HOME/send-matrix-mail)
# spool_dir = "/var/spool/send-matrix-mail"

[matrix]
homeserver  = "https://matrix.example.com"
user_id     = "@bot:example.com"

# Auth: provide a pre-generated access token OR a password for initial login
access_token = "syt_..."
# password = "your_password"   # one-time; token cached on first login

# Default room for unresolvable recipients
default_room = "#alerts:example.com"

[author]
default_from = "noreply@example.com"
default_host = "example.com"
```

### Author resolution precedence

1. `-f <addr>` (CLI flag)
2. `From:` / `Resent-From:` header
3. `$EMAIL` env var
4. `$USER` / `$LOGNAME` @ `default_host` (or `os.Hostname()`)
5. `AuthorConfig.default_from`
6. Error — no deliverable author

## Usage

```
echo "Subject: alert" | send-matrix-mail -t         # recipients from To: header
echo "Subject: alert" | send-matrix-mail user@host   # explicit recipient
send-matrix-mail -t <<< "Subject: test"              # stdin via heredoc
```

### sendmail-compatible flags

| Flag     | Behavior |
|----------|----------|
| `-t`     | Extract recipients from To/Cc/Bcc headers |
| `-f <addr>` | Set envelope-from (author) |
| `-F <name>` | Set sender full name (ignored, accepted for compat) |
| `-i`, `-oi` | Ignore dots in stdin (always on — read to EOF) |
| `-C <path>` | Config file path |
| `--`      | End of flags (no further args parsed as flags) |

All other msmtp/sendmail compat flags are silently accepted.

### Exit codes

| Code | Constant      | Meaning |
|------|---------------|---------|
| 0    | EX_OK         | Message delivered or safely queued |
| 65   | EX_DATAERR    | Input format error |
| 67   | EX_NOUSER     | No deliverable targets |
| 73   | EX_CANTCREAT  | Spool write failure (disk full, permissions) |
| 78   | EX_CONFIG     | Configuration error (missing token, bad config) |

## Offline resilience

When the Matrix homeserver is unreachable or returns a transient error:

1. Message is written to the **local spool** (`$XDG_STATE_HOME/send-matrix-mail/spool/queue/`)
2. Pending messages are retried with **exponential backoff** (60s × 2^attempts, cap 24h)
3. Messages expire after **7 days** and move to `spool/failed/`
4. Each invocation processes up to **60 seconds** of pending messages

Spool layout:

```
<state-dir>/spool/
  tmp/     ← atomic staging area
  queue/   ← pending delivery (body + .meta sidecar)
  failed/  ← expired or permanently failed
```

## Build

```sh
go build ./...
go vet ./...
go test -count=1 ./...
```

## Architecture

Three deep modules, wired by a thin `main.go`:

```
sendmail.Parse  →  (*Envelope, error)
matrix.Client   →  NewClient(cfg) + Send(ctx, env)
queue.Spool     →  Deliver(ctx, env, sendFn)
```

See [`PLAN.md`](./PLAN.md) and [`CONTEXT.md`](./CONTEXT.md) for design rationale and domain terminology.

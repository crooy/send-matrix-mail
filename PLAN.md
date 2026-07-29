# send-matrix-mail — Implementation Plan

## Architecture (deep module design)

A single Go binary. Three deep modules behind small interfaces, wired by a thin composition root.

```
┌──────────┐   stdin + args    ┌──────────────────────────────────────┐
│  caller  │ ────────────────> │  main.go (composition root)          │
└──────────┘                   │                                      │
                               │  env, err := sendmail.Parse(args, in)│
                               │  client  := matrix.NewClient(cfg)    │
                               │  spool   := queue.NewSpool(dir)      │
                               │  spool.Deliver(ctx, env, client.Send)│
                               └──────────────────────────────────────┘

    sendmail.Parse          matrix.Client.Send        queue.Spool.Deliver
    ┌─────────────────┐    ┌──────────────────┐    ┌────────────────────────┐
    │ flags + RFC 5322│    │ resolve + send   │    │ try → enqueue → retry  │
    │ → Envelope      │    │ → m.text per room│    │ owns delivery lifecycle│
    └─────────────────┘    └──────────────────┘    └────────────────────────┘
```

## Tech Stack

| Concern | Choice | Rationale |
|---|---|---|
| Language | Go (≥1.22) | Single static binary, excellent stdlib, no runtime deps |
| Matrix SDK | `maunium.net/go/mautrix` v0.29+ | Maintained, v3 API, context-aware, built-in retry |
| Queue storage | Filesystem spool (`tmp/`→`queue/`→`failed/`) | Zero deps, inspectable, crash-safe via atomic rename |
| Config format | TOML (`BurntSushi/toml`) | Simple, readable, Go-idiomatic |
| CLI parsing | Manual argv walk | sendmail flags don't fit `flag`'s model |

## Module Layout

```
send-matrix-mail/
├── main.go                  # Composition root — wires adapters, 3 calls
├── go.mod
├── go.sum
├── sendmailrc.example.toml
│
├── internal/
│   ├── sendmail/            # Deep module: parse CLI + stdin → Envelope
│   │   ├── parse.go         # Parse(args, stdin) — single entry point
│   │   └── parse_test.go
│   │
│   ├── matrix/              # Deep module: resolve recipients, send m.text
│   │   ├── client.go        # NewClient + Send (Resolve is unexported)
│   │   └── client_test.go
│   │
│   ├── queue/               # Deep module: owns the delivery lifecycle
│   │   ├── spool.go         # NewSpool + Deliver (Enqueue/process are internal)
│   │   ├── spool_test.go
│   │   └── meta.go          # JSON .meta sidecar (unexported)
│   │
│   └── config/              # Infrastructure: TOML loading
│       ├── config.go        # Load() → Config
│       └── config_test.go
```

## Module Interfaces

### `internal/sendmail` — single entry point

```go
// Parse parses sendmail CLI arguments and an RFC 5322 message from stdin.
// Handles -t (extract recipients from headers), -f (author), -F (author name),
// -i/-oi (no-op — always read to EOF), -- (end of flags), and accepts-but-ignores
// the full msmtp compatibility flag set.
//
// Returns the envelope or an *Error carrying a sysexits code.
func Parse(args []string, stdin io.Reader, cfg AuthorConfig) (*Envelope, error)
```

```go
type Envelope struct {
    Author     string   // resolved author address
    Recipients []string // all recipients (argv + header extraction)
    Subject    string
    Date       string
    Headers    string   // raw header block, Bcc stripped
    Body       string   // message body
}

// AuthorConfig provides fallbacks for author resolution.
type AuthorConfig struct {
    DefaultFrom string // config default_from
    DefaultHost string // hostname for $USER@host fallback
}

// Error carries a sysexits code for the caller to use as exit status.
type Error struct {
    Code int    // sysexits code
    Msg  string
}
func (e *Error) Error() string
```

**Author resolution precedence** (internal to Parse):
1. `-f <addr>` (CLI)
2. `From:` / `Resent-From:` header
3. `$EMAIL` env var
4. `$USER@<host>` or `$LOGNAME@<host>`
5. `AuthorConfig.DefaultFrom`
6. Else → `Error{Code: 78}` (EX_CONFIG)

**What's hidden:** flag parsing, RFC 5322 header extraction, folded-header unfolding, Bcc stripping, Resent-* header precedence, sendmail compat flag ignore-list, envelope-from resolution.

**Dependency category:** In-process (stdlib only). Tested directly with string args and `strings.Reader`.

### `internal/matrix` — send, don't expose resolution

```go
type Client struct { /* unexported */ }

// NewClient creates a Matrix client, logging in or refreshing the access token
// as needed. Token state is persisted to $XDG_STATE_HOME/send-matrix-mail/token.json
// (never the config file).
func NewClient(cfg MatrixConfig) (*Client, error)

// Send resolves each recipient to a delivery target (DM → named room → default room),
// deduplicates targets, and sends an m.text message to each unique room.
//
// Errors from Send implement queue.Retryable when the failure is transient
// (network, 5xx, 429). Permanent errors (bad room, auth, user not found) do not.
func (c *Client) Send(ctx context.Context, env *sendmail.Envelope) error
```

```go
type MatrixConfig struct {
    Homeserver  string
    UserID      string
    AccessToken string // pre-existing token, if any
    Password    string // fallback for login
    DefaultRoom string // room ID or alias for fallback delivery
}
```

**What's hidden:** Three-tier recipient resolution, DM creation, room joining, message formatting (header block + body → m.text), per-target txnId generation, error classification into retryable/permanent.

**Seam:** `Client.Send` satisfies `queue.SendFunc`. The `Send` method is the production adapter; tests inject a mock function at the `queue` seam.

**Dependency category:** External (mautrix HTTP client). The `SendFunc` port at the `queue` seam enables testing without a real Matrix server.

### `internal/queue` — owns the delivery lifecycle

```go
// Retryable is implemented by errors that should trigger a retry rather than
// permanent failure.
type Retryable interface {
    error
    Retryable() bool
}

// SendFunc delivers an envelope to Matrix. Errors implementing Retryable
// signal transient failure; all others are permanent.
type SendFunc func(ctx context.Context, env *sendmail.Envelope) error

type Spool struct { /* unexported */ }

func NewSpool(dir string) *Spool

// Deliver attempts to deliver env via sendFn. On success: returns nil.
// On transient failure (sendFn returns Retryable): enqueues to local spool,
// processes pending messages up to a deadline, returns nil.
// On permanent failure: returns the error immediately without queueing.
// On queue-write failure: returns the error (disk full, permissions).
//
// Contract: nil return means the message is handled (delivered or safely queued).
// Non-nil return means the message is lost and the caller should exit non-zero.
func (s *Spool) Deliver(ctx context.Context, env *sendmail.Envelope, sendFn SendFunc) error
```

**What's hidden:** Atomic tmp→queue→failed directory transitions, JSON .meta sidecar, exponential backoff (`60s × 2^attempts`, cap 24h, respect 429 `retry_after_ms`), 7-day expiry, stale tmp/ cleanup on startup, max processing deadline (60s).

**Spool layout:** `<dir>/tmp/`, `<dir>/queue/`, `<dir>/failed/`. One file per message + `.meta` sidecar.

**Dependency category:** Local-substitutable (filesystem). Tests use `t.TempDir()` as spool directory and a mock `SendFunc`.

### `internal/config` — infrastructure, not a deep module

```go
type Config struct {
    Matrix   matrix.MatrixConfig
    Author   sendmail.AuthorConfig
    SpoolDir string
    LogLevel string
}

func Load(path string) (*Config, error)
```

Config search: `-C <path>` → `$XDG_CONFIG_HOME/send-matrix-mail/sendmailrc.toml` → `/etc/send-matrix-mail/sendmailrc.toml`.

### `main.go` — composition root

```go
func main() {
    cfg := config.Load(flagConfigPath())
    env, err := sendmail.Parse(os.Args, os.Stdin, cfg.Author)
    if err != nil { os.Exit(err.(*sendmail.Error).Code) }

    client, err := matrix.NewClient(cfg.Matrix)
    if err != nil { os.Exit(78) } // EX_CONFIG

    spool := queue.NewSpool(cfg.SpoolDir)
    if err := spool.Deliver(context.Background(), env, client.Send); err != nil {
        log.Printf("send-matrix-mail: %v", err)
        os.Exit(73) // EX_CANTCREAT
    }
}
```

Signal handling: on SIGTERM/SIGINT, cancel the context passed to `Deliver` — the spool finishes the current send attempt then returns.

## Implementation Phases

### Phase 1: Scaffold
- `go mod init`, install Go toolchain
- Config TOML loading (`internal/config`)
- Stub out package files, dependency graph

### Phase 2: `internal/sendmail` — Parse
- `parse.go` — single `Parse(args, stdin, cfg)` function
- Flag parsing: honor `-t`, `-f`, `-F`, `-i`/`-oi`, `--`; accept-and-ignore compat flags
- RFC 5322 header extraction, Bcc stripping, Resent-* handling
- Author resolution with fallback chain
- Tests with string args + `strings.Reader`

### Phase 3: `internal/matrix` — Client
- `client.go` — `NewClient` (login, token persistence), `Send` (resolve + format + deliver)
- Unexported `resolveRecipient` → three-tier resolution (DM → room → default)
- Unexported `formatMessage` → header block + body as m.text
- Error wrapping: `queue.Retryable` for transient, plain `error` for permanent
- Tests with httptest server or mock mautrix transport

### Phase 4: `internal/queue` — Spool
- `spool.go` — `NewSpool`, `Deliver` (try → enqueue → process pending)
- Unexported: `enqueue` (atomic tmp→queue), `processAll` (backoff runner), meta.go
- `Retryable` interface, `SendFunc` type
- Exponential backoff: 60s × 2^attempts, cap 24h, respect 429 retry_after_ms
- 7-day expiry, 60s processing deadline
- Tests with `t.TempDir()` + mock `SendFunc`

### Phase 5: `main.go` — Wire
- Composition root: 3 calls as specified in the interface section
- Signal handling: cancel context on SIGTERM/SIGINT
- Exit code mapping

### Phase 6: Verification
- End-to-end smoke test with a real Matrix server
- Queue-and-retry: bring server down, enqueue, bring up, verify delivery
- Edge cases: empty stdin, no recipients, bad room, rate limiting

## Resolved Design Decisions

See `docs/adr/0001-recipient-resolution.md` for the recipient resolution model.

- **Daemon model:** Single-process. Each invocation runs the queue processor inline for up to 60s.
- **Message type:** `m.text`, not `m.notice`. Sendmail semantics: recipients should see delivered mail.
- **Message format:** Plain text with header block (From/To/Subject/Date). HTML `formatted_body` deferred.
- **TxnId:** `<enqueue-id>-<room-hash>` — unique per (message, room), stable across retries.
- **Token storage:** `$XDG_STATE_HOME/send-matrix-mail/token.json` — separate from config file.
- **Terminology:** "Author" = email sender; "Bot identity" = Matrix user that authenticates. See CONTEXT.md.
- **Deep module design:** Three modules with minimal interfaces (`Parse`, `NewClient`+`Send`, `NewSpool`+`Deliver`). The composition root (`main.go`) is 3 calls. Error classification flows through `queue.Retryable` interface.


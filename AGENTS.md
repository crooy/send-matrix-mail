# AGENTS.md — send-matrix-mail

Conventions for human and AI contributors. This file is authoritative for how we work.

## Style
We follow **Tiger Style** (see `.skills/tigerstyle/SKILL.md`), adapted from [TigerBeetle's TIGER_STYLE.md](https://github.com/tigerbeetle/tigerbeetle/blob/main/docs/TIGER_STYLE.md). Design priorities: **safety > performance > developer experience**.

Key rules:
- **Assertions density:** ≥2 assertions per function. Assert preconditions, postconditions, invariants.
- **No recursion.** All loops bounded.
- **Every error handled.** No `_ = err`.
- **70-line function limit.** "Push `if`s up, `for`s down."
- **Zero technical debt.** Do it right the first time.
- **Zero-dependency target.** Stdlib first. Justify every external module.

Go specifics: use `if !cond { panic("reason") }` for programmer errors (not operating errors). Run `go vet`, `gofmt`, `goimports`.

## Version Control: jj (Jujutsu)

We use [jj](https://github.com/jj-vcs/jj) (v0.43+), not git directly. The `.jj/` directory is the repo.

Common operations:
```
jj status          # What's changed?
jj diff            # Show working-copy diff
jj new -m "msg"    # Create a new change with description
jj describe -m "..." # Set/update change description
jj squash          # Squash working copy into parent
jj git push        # Push to git remote
```

Changes are auto-committed. There is no staging area — `jj status` shows working-copy state directly. Every `jj new` creates an anonymous change; use `jj describe` to name it before pushing.

## Architecture & Design

### Domain Model

`CONTEXT.md` is the project glossary. It defines canonical terms (Message, Envelope, Author, Recipient, Delivery Target, Bot Identity, Spool, Resolution, Deduplication). Before inventing new terminology:
1. Check if `CONTEXT.md` already has a term.
2. If a term conflicts with the glossary, call it out — don't silently overload.
3. Update `CONTEXT.md` when new terms are resolved.

Use the `/domain-modeling` skill for active glossary work.

### Deep Modules

We design **deep modules**: large behavior behind small interfaces. See `/codebase-design` skill for vocabulary (module, interface, seam, adapter, depth, leverage, locality).

The three deep modules in this project:
- `internal/sendmail` — `Parse(args, stdin, cfg)` → Envelope
- `internal/matrix` — `NewClient(cfg)` + `Send(ctx, env)` → error
- `internal/queue` — `NewSpool(dir)` + `Deliver(ctx, env, sendFn)` → error

`main.go` is the composition root: 3 calls, pure wiring.

### ADRs

Architectural decisions that are hard to reverse, surprising without context, and the result of a real trade-off go in `docs/adr/`. Format: `NNNN-title.md`. See `docs/adr/0001-recipient-resolution.md` for the template.

## Design Workflow

Before coding, we typically run through:

1. **Research** (`/research` skill) — gather facts from primary sources. Delegated to sub-agents.
2. **Plan** — write or update `PLAN.md` with package contracts, interfaces, error models.
3. **Grill** (`/grill-with-docs` skill) — stress-test the plan against edge cases. Filters questions through RFCs and sendmail compatibility before asking the human.
4. **Domain model** (`/domain-modeling` skill) — sharpen terms, create `CONTEXT.md`.
5. **Codebase design** (`/codebase-design` skill) — evaluate interfaces for depth, test seams, deletion test.
6. **Implement** — phases from `PLAN.md`.
7. **Verify** — smoke test the real thing, not just unit tests.

## Testing

- Tests cross the same seam as callers. The interface is the test surface.
- `sendmail`: tested with `strings.Reader` + string args (in-process).
- `matrix`: tested with mock `SendFunc` at the `queue` seam (external dependency).
- `queue`: tested with `t.TempDir()` spool + mock `SendFunc` (local-substitutable).
- Write tests for behavior, boundaries, invariants, and error paths — not source text.

## Commit Messages

Descriptive, informative. Say what and why. Commit messages live in `jj describe` / `git log`. Pull request descriptions are ephemeral — don't rely on them.

## Sub-agents

The harness supports parallel sub-agents via `/task`. Use them for:
- **Research** — gather API docs, library facts, prior art (`/research` skill = `librarian` agent).
- **Parallel implementation** — fan out independent file edits when work genuinely decomposes.
- **Code review** — review a branch or change set (`/code-review` skill).

Never spawn a single sub-agent and wait idle — that's the same work with extra latency.

## Skills Reference

| Skill | Purpose |
|---|---|
| `tigerstyle` (`.tigerstyle/SKILL.md`) | Coding standards |
| `codebase-design` | Deep module design vocabulary and principles |
| `domain-modeling` | Glossary management, term sharpening |
| `grill-with-docs` | Stress-test plans, create ADRs and CONTEXT.md |
| `research` | Gather facts from primary sources |
| `tdd` | Test-driven development workflow |
| `tigerstyle` (`.skills/tigerstyle/SKILL.md`) | Coding standards |
| `diagnosing-bugs` | Systematic bug diagnosis |
| `resolving-merge-conflicts` | Resolve git/jj merge conflicts |

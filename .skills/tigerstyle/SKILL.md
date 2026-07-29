---
name: tigerstyle
description: Safety-critical coding standards adapted from TigerBeetle's TIGER_STYLE.md for Go projects. Prioritizes safety over performance over developer experience.
disable-model-invocation: false
---

# Tiger Style — adapted for Go

Derived from [TigerBeetle's TIGER_STYLE.md](https://github.com/tigerbeetle/tigerbeetle/blob/main/docs/TIGER_STYLE.md). Design goals: **safety > performance > developer experience**, in that order.

## Safety

### Assertions

Go lacks a built-in `assert`. Use `if !condition { panic("...") }` for programmer errors — conditions that should be *impossible* if the code is correct. Do NOT use panics for expected operating errors (network down, bad input) — those return errors.

- **Average two assertions per function.** Assert arguments, return values, preconditions, postconditions, invariants. A function must not operate blindly on data it has not checked.
- **Pair assertions.** Assert the same invariant at two different code paths. E.g., assert message is valid before writing to spool AND after reading from spool.
- **Assert the positive and negative space.** Assert what you expect AND what you must not see. Bugs live at the valid/invalid boundary.
- **Split compound assertions:** `if !a { panic("a") }; if !b { panic("b") }` — simpler to read, precise on failure.
- **Assert compile-time constants** via `init()` blocks — catch design integrity bugs before execution.

### Control Flow

- **No recursion.** All loops bounded. Every loop has an explicit upper bound or a context deadline.
- **Simple, explicit control flow.** Split compound conditions into nested `if/else`. Split `else if` chains into `else { if { } }`.
- **Handle every error.** 92% of catastrophic failures come from mishandled non-fatal errors ([Yuan et al., OSDI '14](https://www.usenix.org/system/files/conference/osdi14/osdi14-paper-yuan.pdf)). Every `if err != nil` must have a conscious decision.
- **State invariants positively.** Prefer `if index < length` over `if index >= length`.

### Bounds

- **Put a limit on everything.** All slices, maps, loops, queues have explicit maximums. Enforce at construction or ingest, not when they overflow.
- **Use explicitly-sized types** (`int64`, `uint32`) over architecture-dependent types (`int`, `uint`) when the value range matters.
- **Declare variables at smallest possible scope.** Minimize variables in scope to reduce misuse probability.

### Function Design

- **Hard limit: 70 lines per function.** Functions fitting on one screen are provably easier to reason about.
- **Inverse hourglass shape:** few parameters, simple return type, meaty logic between.
- **Push `if`s up, `for`s down.** Centralize control flow in parent functions. Keep leaf functions pure.
- **Centralize state manipulation.** Parent holds state; helpers compute what to change. Keep leaf functions pure.

## Performance

- **Think about performance at design time** — the 1000x wins happen before you can profile.
- **Back-of-the-envelope sketches** for the four resources: network > disk > memory > CPU (slowest first), and their two characteristics: bandwidth, latency.
- **Batch I/O.** Amortize network, disk, memory, and CPU costs.
- **Be explicit.** Don't assume the compiler will optimize away allocations. Extract hot loops into standalone functions with primitive args.
- **No dynamic allocation after initialization** — allocate what you need upfront. This avoids GC pressure, use-after-free, and unpredictable latency. (Go note: pre-size slices/maps; avoid `append` in hot paths.)

## Developer Experience

### Naming

- **Get the nouns and verbs right.** Great names capture what a thing is or does. They show domain understanding. Rethink names that don't fit.
- **Don't abbreviate** unless the variable is a loop index or matrix primitive. Write `recipient`, not `rcpt`.
- **Units/qualifiers last, sorted by descending significance:** `latency_ms_max`, not `max_latency_ms`. Related variables with same-length names line up visually.
- **Callback functions** prefixed with the calling function name: `read_sector` → `read_sector_callback`.
- **Callbacks go last** in parameter lists.
- **Don't overload names** with context-dependent meanings.
- **Prefer nouns** for identifiers that appear in docs/conversation: `pipeline` over `preparing`.

### Comments

- **Always say why.** Code shows what; comments explain the rationale. Every non-obvious decision gets a comment.
- **Comments are prose:** capital letter, space after `//`, full stop. End-of-line comments can be phrases.
- **Test descriptions** explain goal and methodology at the top — helps readers skip to what matters.

### Code Layout

- **`main` goes first** in a file. Important things at the top — files are read top-down.
- **Structs:** fields first, then types, then methods.
- **Group resource allocation/deallocation** with newlines — `acquire` and `defer release` together, blank line after.
- **Go-specific:** run `gofmt`/`goimports`. Standard Go layout. 100-column soft limit.

## Technical Debt

- **Zero technical debt.** Do it right the first time. The second time may not happen. Problems caught in design cost 1x; in implementation 10x; in production 100x.
- **"What could go wrong?"** not "What's wrong?" — proactive, not reactive.

## Dependencies

- **Zero-dependency target.** Every external dependency is a supply-chain, safety, and maintenance liability. Stdlib first. When a dependency is unavoidable, prefer small, well-maintained, pure-Go modules.
- **One adapter → hypothetical seam. Two adapters → real seam.** Don't introduce interfaces for single implementations.

## Tooling

- **Go toolchain is the primary tool.** `go build`, `go test`, `go vet`, `gofmt`. Standardized tooling reduces dimensionality as the team grows.
- **No scripts in Bash** when Go will do. Write build/test scripts in Go or keep them trivial.

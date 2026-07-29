# Changelog

## 0.2.0 — 2026-07-29

### Changed

- **Always deliver to default room.** Messages no longer create DM rooms.
  Recipients are mentioned via `m.mentions` — users receive push notifications
  without a dedicated room per recipient.
- Removed `createDM` and `checkUserExists` (DM room creation code).
- `formatMessage` and `formatMessageHTML` now accept a `mentions []string`
  parameter and display mentioned Matrix user IDs.
- `sendText` includes `m.mentions` in the event content for push notifications.

### Added

- `mentionUserID` helper to extract Matrix user IDs from recipient addresses.
- `TestSendNoDefaultRoom`, `TestFormatMessageNoMentions`, `TestMentionUserID`
  test coverage.
- `.gitignore` with `dist/`.

### Fixed

- `NewClient` respects `cfg.StateDir` for token storage.
- `NewClient` falls back to config `access_token` before attempting password login.
- `loadToken` rejects empty cached tokens.
- Default room is always included, even with explicit recipients.
- Token-cache corruption from test runs no longer breaks delivery (config token
  fallback).

## 0.1.0 — 2026-07-29

Initial release.

### Added

- Drop-in sendmail replacement that delivers RFC 5322 mail as Matrix m.text messages
- Three-tier recipient resolution: DM → named room → default room
- Local filesystem spool with exponential backoff for offline resilience
- HTML-formatted messages (org.matrix.custom.html) with bold header labels
- `--version` flag with build-time version embedding
- Debian packaging with update-alternatives integration
- Arch Linux PKGBUILD
- Cross-compilation targets: amd64, arm64

### Fixed

- User existence check before creating DM rooms (Conduit creates phantom rooms otherwise)
- joinRoom sends empty JSON body `{}` — Conduit rejects POST /join with nil body
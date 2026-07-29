# Changelog

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
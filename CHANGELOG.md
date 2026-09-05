# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions live in git tags only. `go.mod` and any embedded version string are
patched by CI at tag time and are never committed with a real version number.

## [Unreleased]

### Added

- Initial project skeleton: Go module, changelog guard hook and workflow, BZ-1.1 licence.
- `internal/crypt`: post-quantum ready encryption layer.
  - Passphrase mode using Argon2id with tunable, header-recorded parameters.
  - Recipient mode using a hybrid X25519 + ML-KEM-768 KEM, both shared secrets
    bound into a single HKDF transcript so the result stays secure as long as
    either primitive holds.
  - STREAM chunked AEAD (XChaCha20-Poly1305) so a lost chunk does not destroy the
    whole message, with chunk index and final-chunk flag bound as associated data
    to prevent truncation, reordering and chunk-splicing.
- `internal/container`: `NCRY1` container. File metadata (name, size, modification
  time) travels inside the encrypted payload; only the parameters the decoder
  physically needs to bootstrap stay in clear.
- `internal/fec`: erasure coding across frames with soft-confidence erasure
  marking, replacing hard-threshold block codes.
- `internal/modem`: macro-cell modulation with soft demodulation, so per-cell
  confidence reaches the decoder instead of being thrown away by a fixed threshold.
- `internal/profile`: two channel profiles selectable on the command line.
  `archive` maximises density for channels we control, `social` maximises
  survivability through platform re-encoding.
- Command line interface: `keygen`, `seal`, `open`, `estimate`, `profiles`, `version`.
  No interactive-only mode, so the tool can be scripted, scheduled and tested.
  Passphrases are read without echo, or from a file, standard input or an environment
  variable. There is deliberately no `--passphrase` flag.
- Continuous integration on Linux, macOS and Windows, plus a cross-compile matrix for
  six targets, all with CGO disabled.
- Security scanning: govulncheck for dependency CVEs against the Go vulnerability
  database, Trivy for the manifest and configuration view, Gitleaks over the full
  history, and CodeQL over our own code. All four also run weekly, because a CVE is
  rarely published on the day of the commit that introduces the dependency.
- Release workflow producing signed-checksum binaries for linux, darwin and windows
  on amd64 and arm64, triggered by a version tag or a manual run, never by a branch
  push. The version is resolved from the git tag and injected at link time, and the
  workflow fails if the built binary does not report it.
- `docs/FORMAT.md`, a normative specification of the container format, including the
  obligations a reader must meet to be safe.

### Changed

- `golang.org/x/crypto` raised from v0.42.0 to v0.56.0, clearing four CVEs Trivy
  reported against `golang.org/x/crypto/ssh`. That package is not imported here, and
  govulncheck was green throughout, which is the expected disagreement between a
  call-graph scanner and a manifest scanner rather than a false alarm to suppress.

### Fixed

- Argon2id pass count is now bounded on parse. The memory ceiling alone did not close
  the denial of service: a header declaring sixteen million passes hangs a decoder
  just as effectively as one declaring four terabytes of memory. Found because it made
  the test suite take three minutes instead of half a second.

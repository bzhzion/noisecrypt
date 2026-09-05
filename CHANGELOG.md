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

- `internal/modem`: modulation with soft demodulation. Every cell comes back with a
  confidence, the distance from the nearest decision boundary, instead of being
  flattened to a bit by a fixed threshold. Per-frame amplitude calibration recovers
  the crushed black and white levels a video round trip leaves behind.
- `internal/fec`: two-layer erasure coding. An intra-frame code repairs cells so a
  lightly damaged frame still yields a correct shard, and an inter-frame code repairs
  whole dropped frames. Sub-shards are erased in confidence order and validated by
  CRC, never by confidence alone. Frames may arrive out of order, duplicated or
  missing, which is what rate conversion actually does to them.
- `fec.NewLayout` derives the intra-frame granularity rather than taking it as a
  constant, and `Layout.Overhead` derives the redundancy from the layout, so an
  advertised figure cannot drift away from what the code costs.

### Measured

- Soft demodulation, on the social geometry at a 1.75 percent raw byte error rate:
  30 of 32 frames repaired using confidence, 3 without it.
- Error envelope of the social geometry: 25 percent intra parity carries 123 payload
  bytes per frame at 114 percent overhead and survives up to 2.8 percent raw byte
  errors; 50 percent carries 81 bytes at 225 percent overhead and survives up to 4.3
  percent. Both figures come from a test that will fail if they regress.
- Confidence ranks corruption about three and a half times better than chance and
  cannot detect it, which is why the CRC exists. The first version of that test
  demanded eighty percent detection and was simply wrong about what soft decisions
  can do.

### Changed

- Profiles no longer declare their own redundancy. `Redundancy` became a method that
  reads the real layout, and the published figures moved with it: the social profile
  costs 114 percent overhead, not the 40 percent previously advertised. The number
  was aspirational; this one is arithmetic.

- `golang.org/x/crypto` raised from v0.42.0 to v0.56.0, clearing four CVEs Trivy
  reported against `golang.org/x/crypto/ssh`. That package is not imported here, and
  govulncheck was green throughout, which is the expected disagreement between a
  call-graph scanner and a manifest scanner rather than a false alarm to suppress.

### Fixed

- Argon2id pass count is now bounded on parse. The memory ceiling alone did not close
  the denial of service: a header declaring sixteen million passes hangs a decoder
  just as effectively as one declaring four terabytes of memory. Found because it made
  the test suite take three minutes instead of half a second.

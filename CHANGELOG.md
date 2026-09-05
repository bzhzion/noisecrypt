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

- `internal/geometry`: frame drawing and registration. A white quiet zone with a
  black border inside it, located by four edge scans, rather than fiducials and a
  homography: a re-encoding pipeline only ever scales, crops and letterboxes, all
  axis-aligned, so solving for eight degrees of freedom when the channel uses four
  is more variance for nothing. Amplitude calibration comes from robust percentiles
  of the cells themselves and costs no frame real estate.
- `internal/video`: FFmpeg plumbing over a raw greyscale pipe, with no temporary PNG
  directory. Frame dimensions are probed on read rather than assumed, because a video
  that has been through a platform comes back at whatever size the platform chose.
- `internal/codec`: the four layers joined into one pipeline, streaming on the encode
  side so a 1 600-frame encode does not hold three gigabytes of images.
- `modem.Whiten`: energy dispersal before modulation. Without it a frame whose bytes
  begin with zeros draws solid black rows that the registration border cannot be told
  apart from, which is not hypothetical: the frame header starts with a version byte
  and a block index of zero, so the first frame of every encode located two rows short.
- Commands `encode`, `decode` and `simulate`. `simulate` re-encodes a produced video
  at a range of qualities and reports which ones still decode, which is what turns the
  profile table from folklore into measurement.
- CI installs FFmpeg on Linux and Windows and sets `NOISECRYPT_REQUIRE_FFMPEG`, so a
  broken install fails the job instead of silently skipping every video test.

### Measured

- **Real platform round trip, 2026-09-05.** A 45 second Short carrying 160 KiB of
  incompressible payload was uploaded to YouTube unlisted, and every rendition YouTube
  produced was downloaded and decoded. All nine recovered the payload byte for byte:
  1080x1920 in H.264, VP9 and AV1, then 720x1280, 608x1080, 480x854, 360x640, 240x426
  and 144x256. The `social` profile is now marked `Verified`.
  - YouTube cost nothing: the three unreadable frames out of 1344 are the same three
    that failed locally before the upload, so they come from this codec's registration
    rather than from the platform.
  - The profile is heavily overbuilt. Designed for a scaling factor of 8/15, it
    survived 1/7.5, with cells reduced to four pixels and AV1 at a twentieth of the
    source bitrate. A much denser social profile is possible, on measurement.

- Soft demodulation, on the social geometry at a 1.75 percent raw byte error rate:
  30 of 32 frames repaired using confidence, 3 without it.
- Error envelope of the social geometry: 25 percent intra parity carries 123 payload
  bytes per frame at 114 percent overhead and survives up to 2.8 percent raw byte
  errors; 50 percent carries 81 bytes at 225 percent overhead and survives up to 4.3
  percent. Both figures come from a test that will fail if they regress.
- Real H.264, measured by `simulate` on this machine: the `social` profile decoded at
  every quality tested down to CRF 42; `archive` decoded at CRF 18 and 23 and failed
  at 28. Local re-encodes only, so a lower bound on what a platform does.
- Confidence ranks corruption about three and a half times better than chance and
  cannot detect it, which is why the CRC exists. The first version of that test
  demanded eighty percent detection and was simply wrong about what soft decisions
  can do.

### Fixed

- **The intra-frame layout was discarding up to 48 percent of every frame.** Shards must
  all be the same size, so a layout can only address shardCount times shardSize bytes and
  loses the remainder. Choosing the shard count first, as "take 256, the finest
  granularity", maximises the count rather than the utilisation, and the two are not the
  same thing. Found while densifying the social profile: doubling the bits per cell gained
  fifteen percent of payload instead of doubling it, because the extra capacity landed in
  the discarded remainder. `NewLayout` now searches shard sizes and keeps the one that
  wastes least, preferring more shards on a tie. Waste across the four real candidate
  geometries went from 0, 232, 247 and 21 bytes to 0, 0, 0 and 1. Two tests pin both the
  utilisation and the symptom that exposed it.
  - The `social` profile is unaffected, so its verified status still holds. `archive`
    gains slightly: 26 419 payload bytes per frame at 17 percent overhead, up from
    26 108 at 18.

### Added

- `simulate` takes geometry overrides (`-cell`, `-levels`, `-intra-parity`,
  `-inter-parity`) so a candidate can be measured before it is registered as a profile.
  Tuning a channel by editing a constant, rebuilding and eyeballing the result is how
  unverifiable magic numbers get into a codec; this makes measuring the cheap step and
  committing the consequence. Candidates report themselves as `-candidate` and unverified
  on every line.

### Documentation

- README rewritten for someone who has never heard of carrying data in video: what it
  does, why anyone would want it, and how the codec works explained through the picture
  it actually draws rather than through its internals. Leads with the YouTube result,
  keeps every honest limit (not steganography, no sender authentication, losing the key
  loses the data, using a platform as storage likely breaks its terms), and drops the
  comparisons to other tools, which said more about them than about this one.
- Fixed a contradiction the rewrite exposed: the profile section still claimed neither
  profile had been measured, four sections below the summary announcing that `social` had
  been verified against every YouTube rendition.

### Security

Findings from the first full audit (2026-09-05). Semgrep over 82 rules reported four
hits, all `math/rand` in test files where the determinism is deliberate; everything
below came from manual review, and all three were verified by experiment before being
called findings.

- **File names from a container could hit Windows device names and alternate data
  streams.** Sanitising covered path traversal but not Windows naming, and all three
  cases made the tool report a successful extraction while doing something else: a
  reserved name (`NUL`, `CON`, `COM1`, with any extension) wrote 33 bytes without error
  and left zero on disk; a colon (`report.txt:cache`) wrote the payload into a hidden
  stream that Explorer shows as an empty file; a trailing dot or space was stripped by
  the filesystem so the file did not have the announced name. Now rejected, on every
  platform, because the machine that seals is not the machine that opens.
- **No minimum passphrase length when sealing.** A one-character passphrase was accepted
  silently, which makes the Argon2id cost irrelevant: a work factor multiplies the price
  of searching a keyspace, it does not create one. A floor of 8 bytes now applies to
  sealing only, never to opening, so containers made elsewhere still open.
- **Silent integer truncation on command line flags.** `uint8(*kdfLanes)` wrapped, so
  `-kdf-lanes 260` became 4 and the container was sealed at a cost the user never asked
  for and could not notice, with the wrong value written permanently into the header.
  The conversion now refuses instead of truncating.

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

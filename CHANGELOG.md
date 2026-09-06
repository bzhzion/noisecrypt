# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions live in git tags only. `go.mod` and any embedded version string are
patched by CI at tag time and are never committed with a real version number.

## [Unreleased]

### Added

- **A graphical interface**, `noisecrypt gui`, which opens a page in your browser and
  serves it from the binary itself. Encrypt, decrypt and generate identities without a
  command line. Carrying a container as video is still command line only.
  - **Served locally rather than drawn natively**, because every Go toolkit that opens a
    real window links C into the process holding the decrypted private key. A browser is
    C++ too, and far more of it, but it runs in its own process and never sees a key. The
    practical dividend is that the interface cross-compiles to the same six targets as
    the command line, with no C toolchain anywhere.
  - Bound to `127.0.0.1` on a port the operating system picks, never a fixed one, and
    never `:0`, which would put the interface on the network.
  - **Authenticated even though it is local**, because "only local" is not a boundary:
    any page you visit can send requests to `127.0.0.1`, and DNS rebinding can let it
    read the replies. Three checks, each closing something different: the `Host` header
    against rebinding, the `Origin` header against ordinary cross-site requests, and a
    per-run token compared in constant time. Nobody types the token; the binary opens the
    browser with it.
  - Nothing on the page is fetched from anywhere else. A web font would announce, to
    whoever serves it, that this tool is being used.
  - Honours `prefers-reduced-motion`, and every colour pair was measured rather than
    eyeballed: 6.39:1 at the lowest, against the 4.5:1 that WCAG 2.2 AA asks for.

- Minimum Go raised to **1.26.1**. Adding an HTTP server made fifteen standard library
  vulnerabilities *reachable* that had never been reachable before, all of them fixed in
  that release. Worth recording as the moment the dependency scanner paid for itself:
  nothing in this repository's own code changed for the worse, and the finding was real
  anyway.

- **Optional digital signatures**, so a container can prove who produced it rather than
  only that somebody knew the recipient's public key. `-sign` when encrypting, `-from`
  or `-require-signature` when decrypting.
  - Hybrid, on the same reasoning as the key exchange: Ed25519 **and** ML-DSA-65
    (FIPS 204), both produced and both required to verify. There is no mode that accepts
    one of the two.
  - **Optional means optional.** A signature that is present must verify, and a failure
    is fatal rather than a warning, because tolerating a broken one would make stripping
    a signature as effective as forging it. A signature that is absent is not an error;
    the tool says plainly that nothing proves the container's origin, and refusing that
    is the caller's decision through `-require-signature`.
  - The signature is made over the **plaintext**, so it travels inside the ciphertext
    and only the recipient learns who sent the container. Putting it outside would tell
    every observer, which contradicts the point of keeping metadata confidential.
  - What is signed includes **the recipient's fingerprint**, closing surreptitious
    forwarding: a recipient cannot re-encrypt a still-valid signed payload to a third
    party and have it verify there.
- Identities now carry four keys instead of two, because encryption keys cannot sign.
  A public identity goes from 1216 to 3200 bytes, so the pasted token grows from about
  1620 to 4288 characters. The private half stays at 160 bytes by storing seeds rather
  than expanded keys.
- `PublicIdentity.Short`, a grouped 64-bit fingerprint, used wherever a human needs to
  see which identity is involved.
- **Test vectors in `docs/FORMAT.md`**, which the specification had promised would ship
  "with the first tagged release" and did not. A second implementation can now derive one
  reference identity from a published seed and check its length, its token length and its
  fingerprint against fixed values.
  - **No fixed ciphertext vector, deliberately.** Sealing is randomised, so pinning one
    would mean fixing the nonce prefix and the Argon2id salt, and a format that can be
    made deterministic on request invites an implementation that ships that way by
    accident. Nonce reuse under a stream cipher leaks the exclusive-or of two plaintexts,
    which is a poor trade for a static test file. The guarantee is stated in the
    checkable direction instead: a container from any conforming implementation must
    open.

### Fixed

- **The signature did not cover the signer's own encryption keys.** Found by flipping
  every bit of a signed payload rather than by reading the code: byte 53 could be
  changed with the signature still verifying. A signed payload embeds the signer's
  identity, which holds four keys, and only the signing pair takes part in verification,
  so the encryption keys inside it were covered by nothing. An attacker could swap them,
  keep the signature valid, and a recipient composing a reply from that identity would
  encrypt it to the attacker. The signed message now includes the signer's whole
  identity.
- Printing the full 4288 character identity token on every successful decode buried the
  rest of the output. It reports a short fingerprint now.

### Security

- `govulncheck` reports GO-2026-5932 in a module we require: `golang.org/x/crypto/openpgp`
  is unmaintained and unsafe by design. It is **not imported** here, it has no fixed
  version and never will, since the advisory's remedy is to stop using the package.
  Recorded as an accepted risk so a future audit does not re-open it.

## [0.1.0] - 2026-09-05

First tagged release. The pipeline works end to end and both platform profiles have been
through a real YouTube round trip.

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

- **`social-hd`, a third channel profile, verified against YouTube.** 15 pixel cells
  instead of 30, carrying 567 payload bytes per frame against 123, so 40 MiB becomes
  **41 minutes of video instead of 3 hours 9**.
  - **Platform round trip, 2026-09-05**: a 46 second Short carrying 750 KiB, every
    rendition pulled back and decoded. **All ten recovered the payload byte for byte**,
    1080x1920 down to 144x256 across H.264, VP9 and AV1, with **zero unreadable frames
    anywhere**.
  - Two results worth carrying forward. The simulation was **pessimistic** rather than
    optimistic, which is the safe direction to be wrong in: it predicted failure at 320p,
    and YouTube produces no 320p rendition, so the one geometry this profile dislikes
    never arises there. That weakness is real and is why `social` still exists.
  - And the three frames that were always unreadable under `social` are gone. They came
    from the border detector eating a mostly dark first row of data; halving the cell size
    doubles the cells per row and makes such a row far less likely. The faster profile is
    also the cleaner one, which confirms the earlier diagnosis of those three frames.
  - On YouTube specifically `social-hd` is therefore strictly better: same survival, four
    and a half times the payload, fewer lost frames. `social` remains the answer for a
    platform nobody has measured.
  - The floors were found first by simulation: 30 px holds to 256p, 20 px and 15 px to
    426p, 12 px and 10 px only to 640p.
- **`simulate` now downscales as well as re-compresses** (`-heights`), which closes a hole
  the command used to admit to in its own output: it tested what a platform does to
  quality and not what it does to resolution, and resolution is where the failures are.
  Validated by reproducing the real YouTube result locally.
  - This surfaced a genuinely odd behaviour worth knowing about. 15 px cells **fail** at
    320p and **succeed** at 256p: lower resolution, better outcome. At 320p a cell becomes
    2.5 pixels and straddles a boundary; at 256p it becomes exactly 2 and aligns. The rule
    that falls out is about pixels per cell rather than resolution: above roughly six a
    fractional boundary is absorbed, below about three it is fatal. The comment claiming
    divisibility was always load-bearing has been corrected; real renditions at 608x1080
    and 480x854 put cells on fractional boundaries and decoded perfectly.
- `simulate` takes geometry overrides (`-cell`, `-levels`, `-intra-parity`,
  `-inter-parity`) so a candidate can be measured before it is registered as a profile.
  Tuning a channel by editing a constant, rebuilding and eyeballing the result is how
  unverifiable magic numbers get into a codec; this makes measuring the cheap step and
  committing the consequence. Candidates report themselves as `-candidate` and unverified
  on every line.

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
    gains slightly: 26 393 payload bytes per frame at 17 percent overhead, up from
    26 108 at 18.

- Argon2id pass count is now bounded on parse. The memory ceiling alone did not close
  the denial of service: a header declaring sixteen million passes hangs a decoder
  just as effectively as one declaring four terabytes of memory. Found because it made
  the test suite take three minutes instead of half a second.

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

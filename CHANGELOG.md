## [Unreleased]

### Added

- **A graphical interface**, `noisecrypt gui`, which opens a page in your browser and
  serves it from the binary itself. Encrypt, decrypt, carry a file as video, recover one
  from a video, and generate identities, without a command line.
  - **The estimate is a separate button, deliberately.** An encode runs for as long as
    the video is long, with no total to divide by and therefore no honest progress bar,
    so the interface offers to measure the cost before spending it. It measures rather
    than approximates: the container is really sealed, because the overhead depends on
    the mode, the KDF parameters and whether compression helped.
  - A wrong channel is the likeliest mistake this interface allows, and the decoder's own
    error ("no readable frames") does not point at it. The message now quotes both sets
    of dimensions, which is the evidence, and names the two things they can mean.
  - The unreadable-frame count is reported even on success. Redundancy absorbing damage
    silently is the system working, and also exactly what hides a channel getting worse.
  - Missing FFmpeg is reported once when the page loads, rather than by a button that
    fails after a file has been chosen and a passphrase typed.
  - Scratch files are created with `os.CreateTemp` and removed on every path out
    including the failures, with a test that fails if any survive. A handler that returns
    early leaves a copy of the user's video in the temporary directory, which is a
    confidentiality failure rather than untidiness.
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

- The help now names its one prerequisite: `encode`, `decode` and `simulate` shell out to
  FFmpeg and the other seven need nothing installed. A list of commands cannot show that,
  so a user met it as an error instead.

- The interface **shows its version**. It previously said "Local instance", which the
  reader already knew, and left someone reporting a problem unable to say which build
  they were looking at.

- The interface now **states what it cannot do** rather than letting a reader conclude
  those things do not exist: `simulate`, the Argon2id parameters, the encoder knobs, and
  reading a passphrase from a file or the environment.

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

### Changed

- The licence file is now `LICENSE.md`, matching the other public repositories in this
  family and the canonical `BZ-1.1.md` it is copied from. The text is Markdown, so a name
  without an extension made GitHub serve it as preformatted text with the `#` and `**`
  visible. The name has no bearing on licence detection: GitHub reports BZ-1.1 as "Other"
  either way, since it is not an SPDX-listed licence.

- A profile now reports **what it was measured against** rather than a yes-or-no
  `Verified` flag. Three states: `platform` (a container went up to a real service and
  came back through its rendition ladder), `local` (a real encoder, no platform), and
  `unmeasured`, each with a one-line note behind it.
  - The boolean was understating `archive`. It counted platform round trips only, so
    `archive` read as false, and false next to two profiles reading true says "not tested
    yet". The truth is that `archive` targets channels nobody re-encodes, so it has no
    platform to be carried across, and it *was* measured, locally, up to CRF 23. Two
    states cannot hold three situations: "measured elsewhere" and "not measured" were
    collapsed into the same value, which is precisely the kind of claim this package
    exists to avoid making.

- Minimum Go raised to **1.26.6**. Adding an HTTP server made fifteen standard library
  vulnerabilities *reachable* that had never been reachable before: `net/http.Server`
  pulls a large part of the standard library into the call graph, so govulncheck stopped
  discounting them. Nothing in this repository's own code changed for the worse, and the
  finding was real anyway, which is the scanner earning its place.
  - Recorded because the first attempt got it wrong in an instructive way. The report
    lists a `Fixed in:` version per vulnerability, and the fix is the **maximum** across
    all of them, not the first one you read: a bump to 1.26.1 cleared four of the fifteen
    and the build stayed red. A scanner that reports per-finding requirements needs its
    output aggregated before it is acted on.

### Fixed

- **The three unreadable frames the `social` profile lost on every encode are gone**, and
  the cause was not damage. They failed before any channel was involved: the codec could
  not read a frame it had drawn itself a moment earlier.
  - The clue that made this worth chasing rather than accepting: `social-hd` lost none,
    on the same codec with the same parity, differing only in cell size.
  - The cause is statistical, not geometric. `findEdge` walks quiet zone, then border,
    then stops at the first *mixed* line, and it used to give up the moment it met a line
    more than 70% bright while inside the border. But a first line of data can be almost
    all one level by luck, and "mostly" is a fixed threshold applied to a line one cell
    wide, so its spread depends on how many cells are stacked in it. `social` stacks 62
    cells per column: 44 or more bright out of 62 sits 3.3 standard deviations out, one
    line in two thousand, four edges per frame. `social-hd` stacks 124 and puts the same
    event 4.5 standard deviations out, one in 250,000 — which is exactly why it never
    lost a frame where `social` always lost about three.
  - A uniformly bright first line is now held as a candidate and accepted once mixed
    content confirms it, rather than treated as proof that the border enclosed nothing.
    The guard it provided is kept and tested: brightness running to the far edge, and the
    border-gap-border shape, both still fail.
  - Verified on a real re-encode round trip and not only in memory: 0 of 32 frames
    unreadable at 1920p and at 426p, CRF 26 and 34.

- **`social` was losing seven frames per encode on a channel that loses nothing**, and
  nobody knew because the figure that would have shown it did not exist an hour earlier.
  The new loss counter reported it on its first real use, on a 1344-frame video that had
  never been near a platform.
  - The cause was found by an experiment rather than an argument: decode the same frames
    twice, once letting the geometry find the data area and once handing it the exact
    rectangle that was rendered. Seven discarded became zero, which makes location the
    cause rather than a correlate.
  - Dumping the row statistics of an offending frame settled what two rounds of
    reasoning had got wrong in two different ways. The border reads at **0.97 dark** and
    an unlucky first line of data at **0.75**, and the threshold that separates border
    from data is 0.70, so that line is absorbed into the border and the rectangle ends up
    exactly one cell short on one edge.
  - **Not fixed by moving the threshold.** The two are separable on a frame straight out
    of `Render` and a platform's blur closes the gap; the threshold is low precisely to
    survive the platform. Bounding the dark run by the observed quiet run fails the same
    way, on cropped input.
  - Fixed one level up, where the information exists. Cells are square in a rendered
    frame and a channel only rescales, crops and letterboxes, so a correct rectangle
    measures the same cell size across its width as across its height, and one swallowed
    line breaks that by exactly one cell. `codec.Decoder` knows cols and rows, so it
    offers the two corrected rectangles as **second readings** and lets the CRC pick —
    the arbiter this codec already trusts everywhere else, rather than a new constant.
  - Second readings are flagged, so a guess that does not pay off is not counted as a
    lost frame. A loss counter that moves for reasons unrelated to the channel is worse
    than no counter.
  - Measured end to end on the same video file: **7 discarded before, 0 after**, and both
    profiles still recover the payload byte for byte. Re-verified against real re-encodes
    at 1920p, 854p and 426p, CRF 26 through 42: 0 unreadable everywhere.

- **The frame-loss figure was quietly optimistic, and it is the one figure meant to
  reveal a channel getting worse.** Two different losses exist and only one was counted:
  frames the geometry could not *locate*, and frames it located and sampled whose shard
  then failed its CRC under every erasure combination the parity allowed. The second were
  dropped at `if !ok { continue }` and counted nowhere, so a video that had genuinely lost
  frames could be reported as losing none, in the command line and in the interface alike.
  - `fec.Stats` now separates `Unparseable`, `Unrepairable` and `OutOfRange`, and both
    surfaces report the total. `Decode` keeps its signature; `DecodeStats` is the new
    entry point, so nothing had to be touched to gain the counter.
  - This corrects a claim made a few entries above, on the interface's unreadable-frame
    count: a mislocated frame does **not** make the error-correcting layer spend parity on
    noise. The per-frame CRC already turns it into an erasure, which is the cheap kind of
    loss. What was actually broken was the reporting, not the correction.

- **`-help` did not work.** Help answered to `-h`, `--help` and `help`, and not to
  `-help`, which is the spelling Go's own flag package prints and therefore the one a Go
  user tries first: it exited 2 on "unknown command". Neither did `-version`, `--version`
  or `-v`. All of them now do. A help flag that has to be guessed correctly is not help.

- **The interface could report a signature but never demand one**, which made it strictly
  weaker than the command line rather than merely smaller. It said who signed a container,
  or that nobody had, and handed the contents over either way, so removing a signature
  worked as well as forging one. Both decrypt forms now carry `requireSignature` and
  `from`, with the five outcomes pinned by tests, including a valid signature by the
  wrong person, which a check on "is it signed" alone waves through.

- **The tab list claimed a keyboard behaviour it did not have.** `role="tab"` tells a
  screen reader the arrow keys move between tabs and that one stop holds them all in the
  page's tab order. Neither was true: four separate stops, arrows inert. Plain buttons
  with no role would have been *more* usable than the pattern half-applied. Now a roving
  tabindex with arrows, Home and End, per the ARIA authoring practices.

- **Steps were spans, so the page had one heading.** No navigation by structure, and the
  video panel holds two sections that had no heading at all.

- **Buttons were styled by their HTML type**, which worked until a form had two of them.
  `button[type="submit"]` outranked `.row button` on specificity and `.action` did not,
  so the two sat at different heights, and the outlined style made the *measuring* button
  look heavier than the *acting* one. Importance is not something an attribute knows:
  buttons now declare it, one filled primary per form.

- **Horizontal scrolling at 320 CSS pixels**, WCAG 1.4.10, caused by the five column
  profiles table. Squeezing it would destroy the comparison it exists for, and the
  criterion exempts content that needs two dimensions, so it scrolls in its own region
  instead. That region is focusable, which is the part usually forgotten: a scroll
  container nobody can focus is one nobody can scroll without a mouse.

- The compression checkbox was 13 px, under the 24 px floor of WCAG 2.2 AA 2.5.8.

- **A tab was being eaten at narrow widths, silently.** The bar was four items across
  inside `overflow: hidden`, which was there so the corner radius would cut the buttons,
  and flex items refuse to shrink below their own text: at 320 CSS pixels the row
  measured 316 px inside a 273 px box and the last tab was clipped away with nothing on
  screen to say so. It now wraps two by two, stated in a breakpoint rather than left to
  emerge from the available width, which had produced a three-plus-one at 480 px with the
  fourth tab stretched alone across its own row. Sideways scrolling was rejected: it
  hides content behind a gesture instead of behind a clip.

- The header committed to two columns at every width, leaving 16 px between the tagline
  and the readout on a phone. It stacks below 34rem.

- **The body copy was set in absolute pixels** while everything around it was in rem, so
  browser zoom scaled the whole interface and left the reading size behind, and a reader
  who raises their default font size instead of zooming got nothing. Found by verifying
  that a zoom test had actually zoomed, rather than trusting that it had.

- Verified by driving a browser at 320, 360, 480, 544, 560, 768 and 1280, and again at
  200 percent text zoom, rather than by reading the stylesheet.

- New `markup_test.go`: labels, tab and panel wiring, headings, keyboard handling and
  button roles, each proved red against the real defect before being kept. One of those
  proofs failed and improved the test: prefixing `ArrowRight` to `XArrowRight` did not
  turn it red, because a substring check accepts that.

- `PublicIdentity.Short`, a grouped 64-bit fingerprint, used wherever a human needs to
  see which identity is involved.

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

### Known limitations

- **A first line of data that happens to be almost all *dark* is still absorbed into the
  border**, and this one produces no error at all. The located rectangle is one cell
  short, the frame yields bytes rather than a gap, and the error-correcting layer spends
  parity on noise where it could have skipped an erasure. Found by a diagnostic that
  compared the located rectangle against the rendered one instead of only counting
  failures, which is the part that had never been looked at.
  - Cost measured rather than guessed: roughly one frame in a few hundred, worth about
    0.4% raw byte errors on a profile that tolerates 2.77%.
  - **Not fixed here, deliberately.** The obvious fix is a thickness ratio, since `Render`
    puts the border at exactly `margin/2`, the same thickness as the quiet zone outside
    it. It works on a frame straight out of `Render` and breaks the case this layer exists
    for: a platform that crops into the quiet zone leaves three lines of quiet against
    fifteen of legitimate border, and the ratio then fires on every frame and returns a
    position that is wrong by design. Trading a once-in-a-few-hundred fluke for a
    systematic failure on cropped input is a bad trade.
  - The fix that would hold is one level up and is not a ratio. The rendered data area has
    a known aspect ratio of cols to rows, a uniform resize preserves it, and swallowing
    one line deviates from it by 1/rows, about 1.6% on `social`. `Locate` cannot check
    that because it is handed only an image, but `Sample` already receives cols and rows,
    so the check belongs there, acting on a measurable inconsistency rather than a guess
    about thickness.
  - Kept as a skipped test carrying the whole diagnosis, so it stays findable rather than
    becoming folklore.

### Measured

- **There is no third, denser social profile**, and that is now a result rather than an
  omission. `social`'s own notes asked for one after it survived far more than it was
  designed for; `social-hd` was that answer, and the search for a further one stopped on
  evidence.
  - Only three things trade for payload, and two are already at their limit. **Amplitude
    collapses**: four levels instead of two failed at 1920p CRF 42 and at nearly every
    rendition below, because amplitude detail is exactly what a compressor removes first.
    **Cell size has a floor near three pixels after the worst downscale**: ten-pixel
    cells died at 426p and below, where they land at 2.2 px, while fifteen land at 3.3 px
    and survive.
  - The third is parity, and it is the one worth reading twice. A local re-encode sweep
    says halving the budget is free: same qualities decode, same ones fail, cliff at
    CRF 34 either way, a third more payload. **It is not free.** A local x264 re-encode
    fails off a cliff, so it never produces the partial damage parity exists for. Priced
    against raw byte error rate instead, the same cut takes tolerance from **2.77% to
    0.68%** — a third more payload for a quarter of the margin.
  - New test `fec.TestParityBuysErrorTolerance` measures the whole range and fails if
    less parity ever stops meaning less tolerance, which would mean the profiles are
    tuned against a knob that does nothing.
  - Worth carrying beyond this repository: **a bench whose failure mode is
    all-or-nothing cannot price redundancy**, because redundancy only pays in the regime
    that bench skips over.

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

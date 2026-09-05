# NoiseCrypt

Encrypt a file into a container designed to survive a hostile channel, then carry it
as video.

Any file goes in: text, Markdown, JSON, PDF, archives, binaries. The format is byte
oriented and knows nothing about what it carries.

> **Status.** The encryption and container layers are complete and tested. The video
> codec is not written yet, so today the tool seals and opens `.ncry` containers and
> tells you what encoding one would cost. See [Roadmap](#roadmap) for what lands next
> and in what order.

## Why

Encryption tools that hide data in video already exist. The ones the author looked at
share three problems, and this project is a reaction to all three.

**They are not encryption.** They encode, and their documentation tells you to encrypt
the file yourself beforehand. That is honest, and it is also a trap: eventually
somebody puts something sensitive in a public video because the tool never stopped
them. NoiseCrypt encrypts by default and has no mode that does not.

**Their tests prove nothing.** A CI job running `go test ./...` against a repository
containing zero test files goes green forever. That is worse than having no CI at all,
because it manufactures confidence with nothing underneath it.

**Their tuning constants cannot be checked.** A magic cell size chosen because it
happened to work once is indistinguishable from a guess, and nobody downstream can
verify it. Every channel parameter here is marked as measured or not measured, and
`noisecrypt simulate` will exist to move numbers from the second category to the first.

## Security design

The full rationale lives in the package documentation of `internal/crypt`. The short
version:

**Post-quantum, not "quantum".** The realistic threat is an adversary recording
ciphertext today and decrypting it years from now. Symmetric cryptography is already
fine against that: Grover's algorithm costs a square root, so a 256-bit key still
leaves a 128-bit margin. Key exchange is not fine, because Shor's algorithm breaks
X25519 outright.

**Hybrid, never lattice alone.** Recipient mode runs X25519 and ML-KEM-768 (FIPS 203)
together and feeds both shared secrets into one HKDF transcript. The result holds as
long as *either* primitive holds. ML-KEM is young and its security estimates still
move; X25519 has a known quantum break. Choosing one alone means betting on a fresh
lattice assumption or betting that quantum computers never arrive.

**Chunked authentication, not one tag over the whole file.** A single AEAD tag over a
whole archive means one damaged byte destroys everything, which defeats the erasure
coding that makes this format work in the first place. NoiseCrypt uses the STREAM
construction: each chunk is sealed separately with its index and an end-of-stream flag
bound into the nonce and the associated data. Chunks cannot be reordered, dropped from
the end, or spliced in from another message.

**Metadata is confidential.** The file name, its size and its modification time live
inside the ciphertext. The cleartext header says how to decrypt, never what was
encrypted. There is a test that fails if the file name appears anywhere in the sealed
bytes.

### What this does not do

This is **not steganography and offers no deniability**. A video of visual noise is
the least discreet object you can upload. Encryption protects the content, not the
fact that content exists. Anyone looking at the file knows immediately that something
is hidden in it, and in some situations that alone is the risk that matters.

It also does not authenticate the sender. A container sealed to your identity proves
someone knew your public key, not who. Signatures are on the roadmap.

## Install

Download a binary for your platform from the
[releases page](https://github.com/bzhzion/noisecrypt/releases), or build from source:

```sh
git clone https://github.com/bzhzion/noisecrypt
cd noisecrypt
make hooks   # git never transports hook configuration; run this after every clone
make build
```

Requires Go 1.25 or later. There is no CGO, so cross-compiling to any of the six
supported targets is a single `GOOS=… GOARCH=… go build`.

## Usage

Everything is reachable from flags. There is no interactive-only mode, deliberately:
a tool you cannot script is a tool you cannot put in a backup job, a pipeline, or a
test.

### Seal with a passphrase

```sh
noisecrypt seal -in report.pdf
noisecrypt open -in report.pdf.ncry
```

The passphrase is read from the terminal without echo. There is no `--passphrase`
flag on purpose: a passphrase on a command line lands in your shell history and in
the process list, where any other user on the machine can read it. For scripts:

```sh
noisecrypt seal -in report.pdf -passphrase-file secret.txt
echo "$PASS" | noisecrypt seal -in report.pdf -passphrase-file -
noisecrypt seal -in report.pdf -passphrase-env NOISECRYPT_PASS
```

### Seal to a recipient

```sh
noisecrypt keygen -out alice.key            # prints the public identity to share
noisecrypt seal -in report.pdf -to 'noisecrypt-public-v1:…'
noisecrypt open -in report.pdf.ncry -identity alice.key
```

`-to` and `-identity` accept either the token itself or a path to a file containing
one, so you do not have to remember which you have.

### Know the cost before paying it

```sh
noisecrypt estimate -in big-archive.zip
```

Reports, per channel profile, how many frames and how many minutes of video the file
becomes. On this kind of channel the ratio between input size and video duration is
the single most important property and the one nobody can guess. It is also why the
answer arrives before the encode rather than after.

### Channel profiles

```sh
noisecrypt profiles
```

| Profile | For | Trade |
|---|---|---|
| `archive` | Channels that do not re-encode: disk, object storage, USB, torrent | Dense. Small cells, four amplitude levels, low parity. |
| `social` | Platforms that re-encode: vertical video, heavy downscaling, deblocking | Robust. Large cells, two levels, 40 percent parity. |

The `social` cell size is 30 pixels, and that is not arbitrary. A platform scaling
1080 down to 576 applies a factor of 8/15; a cell size that is not a multiple of 15
lands on fractional pixel boundaries after the resize and smears every cell edge into
its neighbour. 30 × 8/15 = 16 exactly. The rule generalises: pick a cell size
divisible by the denominator of the platform's scaling factor.

Neither profile is measured yet. `estimate` says so on every line it prints.

## Development

```sh
make check   # gofmt, go vet, go test -race
make vuln    # govulncheck against the Go vulnerability database
```

CI runs the same checks on Linux, macOS and Windows, cross-compiles all six targets,
and runs three security scanners on every push and once a week. The weekly schedule is
the part that matters: a CVE is almost never published on the day of the commit that
introduces the dependency, so a repository scanned only on commit stops being scanned
the moment it stops changing, and silence reads as safety.

Releases are cut from version tags only. Nothing publishes on a branch push.

## Roadmap

Ordered by what unblocks the most.

1. **Video codec.** Modulation with soft demodulation, erasure coding across frames,
   geometric recovery from corner fiducials, FFmpeg muxing. The design is fixed:
   per-cell confidence reaches the decoder instead of being destroyed by a fixed
   threshold, and a fountain-style code decodes from any sufficient subset of frames
   rather than from a rigid block layout.
2. **`noisecrypt simulate`.** Re-encode a produced video locally at several
   compression levels and report whether it still decodes. This is what turns the
   profile table from folklore into measurement, and it is the feature the author
   most wants to exist.
3. **Signatures.** ML-DSA-65 paired with Ed25519, same hybrid reasoning as the KEM,
   so a container proves who sealed it and not merely that someone could.
4. **Graphical interface**, on Linux, macOS and Windows. The command line stays the
   reference implementation and the interface calls into the same packages; a GUI
   that reimplements the pipeline is a second pipeline to keep correct. The framework
   choice is open and has one hard constraint: it must not force CGO on, because
   pure-Go builds are what make the six-target release a single matrix.

## Licence

[BZ-1.1](LICENSE), the BREIZHZION Personal Use License. Source available, not open
source in the OSI sense: personal use by a natural person, no fabrication or service
provision for third parties, forks permitted for reading and study only. Commercial
use is reserved to BREIZHZION or to a holder of a written commercial licence.

Copyright © 2026 BREIZHZION.

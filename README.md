# NoiseCrypt

**Turn any file into a video. Send the video anywhere. Get the file back, byte for byte.**

Your file goes in. What comes out is a clip of flickering grey static, unwatchable and
meaningless to look at. Upload it, download it, and NoiseCrypt hands you the original
file back, identical to the last bit. It is encrypted the whole way, and it is built to
survive being crushed by whatever the video travelled through.

> We uploaded a 160 KiB file to YouTube as a 45 second Short. YouTube re-encoded it into
> nine different versions, shrinking one of them from 1080 pixels wide down to 144 and
> compressing it to a twentieth of its original bitrate.
>
> **All nine gave the file back perfectly.** [See the numbers.](#the-proof)

---

## Why would anyone do this

Because video goes places files cannot.

Plenty of channels will happily accept a video and refuse everything else. A messaging
app that strips attachments. A platform with no file hosting. A workflow where the only
thing that reliably survives is a clip. Video is the universal envelope of the modern
internet, and NoiseCrypt is a way to put something inside it.

There is also a longevity argument. An `.mp4` is one of the most boringly well supported
formats ever made. Software that can play a video will still exist long after the
proprietary archive format you were counting on has stopped shipping a reader.

And sometimes the honest answer is curiosity. Squeezing data through a channel designed
for something else is a satisfying engineering problem, and this one has a clean way to
tell whether you have solved it: either the file comes back identical or it does not.

**One thing to be clear about.** Using someone's video platform as free file storage
almost certainly breaks their terms of service. NoiseCrypt does not care what you point
it at, but you should read the rules of wherever you are uploading.

---

## See it work

Install [FFmpeg](https://ffmpeg.org/) and grab a binary from the
[releases page](https://github.com/bzhzion/noisecrypt/releases). Then:

```sh
# Turn a file into a video. You will be asked for a passphrase.
noisecrypt encode -in holiday-photos.zip -out holiday.mp4

# ...move holiday.mp4 wherever you like...

# Turn the video back into the file.
noisecrypt decode -in holiday.mp4 -out holiday-photos.zip
```

That is the whole tool in two commands. Everything else is control over how it behaves.

Curious what it will cost before you commit? Ask first:

```sh
noisecrypt estimate -in holiday-photos.zip
```

It tells you how many frames and how many minutes of video your file becomes, per
profile, before spending a second of encoding.

---

## How it works, in plain terms

### Step one: the file becomes a picture

Imagine a chessboard. Each square is either black or white, and each square carries one
bit of your data. Fill the board, and you have a frame. Fill fourteen hundred boards and
play them at thirty per second, and you have a video.

That is genuinely it. The "static" you see is your file, drawn as squares.

### Step two: the squares have to survive being flattened

Here is the hard part. A video platform does not store what you gave it. It shrinks it,
blurs it, throws away detail to save bandwidth, and re-compresses it two or three times
on the way to a viewer's screen. Small squares blur into their neighbours and the bits
turn to mush.

So the squares are made deliberately large, and their size is chosen to match how
platforms shrink video. NoiseCrypt draws them at 30 pixels, because a platform that
scales a 1080 pixel wide video down to 576 is dividing by 15 and multiplying by 8, and
30 survives that arithmetic as exactly 16 pixels. No fractional pixels, no bleeding
edges. Pick the wrong size and every square smears into the next one.

### Step three: redundancy, so damage does not matter

Some squares will still be read wrong. Some entire frames will vanish, because platforms
change frame rates and drop frames to do it.

NoiseCrypt therefore spreads your file across the video with mathematical redundancy,
the same family of technique that lets a scratched CD play or a QR code work with a
corner torn off. There are two layers of it, because a video breaks data in two
unrelated ways: scattered wrong squares inside a frame, and whole frames going missing.
One layer repairs each.

The result is that a decoder does not need a perfect video. It needs enough of one.

### Step four: it was encrypted before any of that happened

Before your file is ever drawn as squares, it is compressed and encrypted. Not as an
option you can forget to switch on: there is no mode that skips it.

The file name, its size and the date you last touched it are encrypted too. The video
reveals that *something* is inside, never *what*.

---

## The proof

Talk is cheap on this kind of claim, so here is a real round trip.

A 160 KiB file of pure random bytes, chosen because random data cannot be compressed and
is therefore the hardest case. Encoded into a 45 second vertical video, uploaded to
YouTube as an unlisted Short, then every single version YouTube generated was downloaded
and decoded.

| What YouTube served | Codec | Shrunk to | Squares became | Result |
|---|---|---|---|---|
| 1080x1920 | H.264 | full size | 30 px | ✅ perfect |
| 1080x1920 | VP9 | full size | 30 px | ✅ perfect |
| 1080x1920 | AV1 | full size | 30 px | ✅ perfect |
| 720x1280 | H.264, VP9 | 67 % | 20 px | ✅ perfect |
| 608x1080 | AV1 | 56 % | 16.9 px | ✅ perfect |
| 480x854 | H.264 | 44 % | 13.3 px | ✅ perfect |
| 360x640 | H.264 | 33 % | 10 px | ✅ perfect |
| 240x426 | H.264 | 22 % | 6.7 px | ✅ perfect |
| 144x256 | H.264 | 13 % | 4 px | ✅ perfect |

"Perfect" means the recovered file had the same SHA-256 hash as the original. Not
similar. Identical.

The most extreme row is worth sitting with. At 144x256, each of those carefully sized
30 pixel squares had been crushed to about 4 pixels, and the whole clip was down to
199 kbit/s. It still worked.

**What this does not prove.** One file, one platform, one day. It says nothing about
other platforms, about hour-long videos where frame rates get converted differently, or
about what YouTube's pipeline will do next year. Anyone who tells you a single successful
test is a guarantee is selling something.

---

## The encryption

If you only remember one line: **NoiseCrypt is built for the person who assumes their
encrypted data will be recorded today and attacked in twenty years.**

### Why that matters

Today's key exchange has a known expiry date. A sufficiently large quantum computer
breaks the mathematics behind most of the encrypted traffic on the internet, and
"harvest now, decrypt later" is a real strategy: store the ciphertext, wait for the
machine. For anything you want secret for a decade, that is the threat that counts.

### What NoiseCrypt does about it

**Two locks, not one.** When you encrypt to someone's public key, NoiseCrypt performs
*two* independent key exchanges at once, X25519 and ML-KEM-768 (the post-quantum standard
published by NIST), and blends both results into the final key.

Break one and you get nothing. You need both.

That belt-and-braces design is deliberate. X25519 is decades old and thoroughly studied,
and a quantum computer defeats it. ML-KEM resists quantum attack, and it is young enough
that the security community is still refining its estimates. Betting everything on either
one is a bet. Requiring both is not.

**Passphrase mode needs no such trick.** If you encrypt with a passphrase, the maths
involved is already quantum resistant. NoiseCrypt stretches your passphrase with
Argon2id, which deliberately burns memory and time so that guessing at scale becomes
expensive.

It also refuses to encrypt with a passphrase shorter than 8 bytes, and the reason is
worth understanding: making each guess expensive only helps if there are many guesses to
make. No amount of stretching saves a passphrase of "a". Decryption never enforces the
limit, so a container made elsewhere always opens.

**Damage does not become total loss.** Your file is encrypted in chunks rather than as
one indivisible block. A single damaged region does not render everything unreadable,
which is exactly what makes the redundancy above worth having. The chunks are still tied
together cryptographically, so nobody can reorder them, cut the end off, or splice in
pieces from a different file without the decryption failing loudly.

### What NoiseCrypt is not

**It is not hiding.** A video of grey static is the single most conspicuous thing you can
upload. Anyone who looks at it knows immediately that something is concealed inside.
NoiseCrypt protects *what* your data is, never *that it exists*. If being noticed is
itself your risk, this is the wrong tool and no amount of encryption changes that.

**It does not prove who sent it.** A container encrypted to your key proves that someone
knew your public key. It does not prove who. Digital signatures are on the roadmap.

---

## Everything you can do with it

Every feature is a flag, and nothing hides behind an interactive menu. That is on
purpose: a tool you cannot script is a tool you cannot put in a nightly backup job.

### Encrypt with a passphrase

```sh
noisecrypt encode -in report.pdf -out report.mp4
noisecrypt decode -in report.mp4 -out report.pdf
```

You will be prompted, and what you type is not shown. There is no `-passphrase` flag,
deliberately: anything typed on a command line lands in your shell history and is visible
to every other user on the machine through the process list. For automation, choose a
source that does not leak:

```sh
noisecrypt encode -in report.pdf -passphrase-file secret.txt
echo "$PASS" | noisecrypt encode -in report.pdf -passphrase-file -
noisecrypt encode -in report.pdf -passphrase-env NOISECRYPT_PASS
```

### Encrypt for a specific person

```sh
# Once, on the recipient's machine. Keep the key file, share the printed public identity.
noisecrypt keygen -out alice.key

# Anyone can now encrypt for Alice, with nothing secret shared in advance.
noisecrypt encode -in report.pdf -to 'noisecrypt-public-v1:...'

# Only Alice can open it.
noisecrypt decode -in report.pdf.mp4 -identity alice.key
```

`-to` and `-identity` take either the token itself or a path to a file containing one, so
you never have to remember which form you are holding.

> **Keep that key file safe and backed up.** There is no recovery, no reset and no back
> door. Lose `alice.key` and everything encrypted to it is gone permanently. That is the
> point of the design, and it is also a real way to lose your data.

### Skip the video

Sometimes you just want the encryption. `seal` and `open` do the crypto without producing
a video, writing a compact `.ncry` file instead:

```sh
noisecrypt seal -in report.pdf
noisecrypt open -in report.pdf.ncry
```

### Choose how tough the video needs to be

```sh
noisecrypt profiles
```

| Profile | Use it when | Data per frame | Overhead | A 40 MiB file becomes |
|---|---|---|---|---|
| `archive` | Nothing will re-encode the video: a hard drive, a USB stick, cloud storage, a torrent | 26 419 B | 17 % | 54 seconds |
| `social` | A platform will chew on it: vertical video, heavy shrinking, repeated compression | 123 B | 114 % | 3 hours 9 minutes |

The gap is dramatic and it is the central trade of the whole tool. Toughness is bought
with time. `archive` assumes nobody will touch the file and packs data tightly. `social`
assumes the worst and spends most of the frame on redundancy.

Pick with `-profile`, and use the same one at both ends:

```sh
noisecrypt encode -in big.zip -out big.mp4 -profile social
noisecrypt decode -in big.mp4 -out big.zip -profile social
```

Those numbers are calculated from the actual error-correcting layout rather than written
down beside it, so they cannot drift out of date when the code changes.

### Test the toughness yourself

```sh
noisecrypt simulate -profile social
```

This encodes a payload, re-compresses the video at a range of qualities, and reports
which ones still decode. It exists so that nothing here has to be taken on faith. Run it
on your own machine and see for yourself.

Bear in mind it is a local test: a real platform also rescales and changes frame rates,
so treat the results as a floor rather than a ceiling. The command prints that caveat
itself.

---

## Install

**Binaries.** Grab one from the [releases page](https://github.com/bzhzion/noisecrypt/releases).
Linux, macOS and Windows, on both Intel and ARM.

**FFmpeg is required** for anything involving video. NoiseCrypt looks for it on your
`PATH` and in the usual installation directories, including the awkward versioned folder
that `winget install Gyan.FFmpeg` leaves behind without adding anything to your `PATH`.

**From source**, if you prefer:

```sh
git clone https://github.com/bzhzion/noisecrypt
cd noisecrypt
make hooks   # git never carries hook configuration; run this after each clone
make build
```

Needs Go 1.26 or later. There is no C code anywhere in the project, which is why a single
build command targets all six platforms.

---

## What is coming

1. **A denser `social` profile.** The YouTube results showed enormous unused margin: the
   profile was designed for a shrink factor it beat by a wide distance. Reclaiming that
   margin turns those 3 hours per 40 MiB into something far more usable.
2. **Digital signatures**, so a container proves who created it and not merely that
   somebody could have. Post-quantum and classical together, on the same reasoning as the
   key exchange.
3. **A graphical interface** for Linux, macOS and Windows, for the times a command line is
   the wrong answer.

---

## For developers

```sh
make check   # formatting, static analysis, tests with the race detector
make vuln    # check dependencies against the Go vulnerability database
```

Continuous integration runs those on Linux, macOS and Windows, cross-compiles all six
release targets, and runs four independent security scanners on every push **and once a
week**. The schedule is the part that matters: vulnerabilities are rarely published on the
day the dependency is added, so a project scanned only when it changes stops being scanned
the moment it goes quiet, and silence is easy to mistake for safety.

The container format is specified in [`docs/FORMAT.md`](docs/FORMAT.md) in enough detail to
write an independent implementation. The reasoning behind each cryptographic choice lives
in the package documentation of `internal/crypt`.

## Licence

[BZ-1.1](LICENSE), the BREIZHZION Personal Use License. Source available rather than open
source: personal use by an individual is free, forks are permitted for reading and study,
and commercial use or providing a service based on it is reserved to BREIZHZION or to a
holder of a written commercial licence.

Copyright © 2026 BREIZHZION.

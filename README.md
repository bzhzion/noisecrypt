# NoiseCrypt

**Turn any file into a video. Send the video anywhere. Get the file back, byte for byte.**

Your file goes in. What comes out is a clip of flickering grey static, unwatchable and
meaningless to look at. Upload it, download it, and NoiseCrypt hands you the original
file back, identical to the last bit. It is encrypted the whole way, and it is built to
survive being crushed by whatever the video travelled through.

> We uploaded files to YouTube as unlisted Shorts. YouTube re-encoded each one into
> around ten different versions, shrinking one of them from 1080 pixels wide down to 144
> and compressing it to a fraction of its original bitrate.
>
> **Every single version gave the file back perfectly.** [See the numbers.](#the-proof)

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
| 1080x1920 | H.264, VP9, AV1 | full size | 30 px | ✅ perfect |
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

### And then the fast profile did the same thing

The test was repeated with `social-hd`, which uses squares half the size and carries
**four and a half times as much data**. A 750 KiB file, 46 seconds of video, every
rendition downloaded again.

**All ten came back identical, down to 144x256, and this time not a single frame was
unreadable anywhere.** The tougher profile loses three frames out of 1376 to its own
registration; halving the square size puts twice as many squares in every row, which
makes the situation that causes those losses far less likely. The faster profile is also
the cleaner one.

So on YouTube, `social-hd` is simply the better choice: identical survival, four and a
half times the throughput. `social` keeps its place as the answer for a platform nobody
has measured yet.

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

### Proving who sent it

Encryption alone does not answer that question: a container encrypted to your key only
proves someone knew your public key, not who they were. Signing does, and it is
optional.

```sh
# Alice signs with her own key while encrypting to Bob.
noisecrypt encode -in report.pdf -to '<Bob's public identity>' -sign alice.key

# Bob accepts it only if Alice really signed it.
noisecrypt decode -in report.mp4 -identity bob.key -from '<Alice's public identity>'
```

On success you get a short fingerprint rather than a wall of base64:

```
Recovered report.pdf (7.1 KiB).
  Signature verified, signed by 18cd-3849-0f60-5f59
```

Two signatures are made and both must check out: Ed25519 and ML-DSA-65, the
post-quantum standard. Same reasoning as the two locks above, applied to authorship.

Three rules are worth knowing because they are what makes the feature mean anything:

- A signature that is **present must verify**. A broken one is a hard failure, never a
  warning, because otherwise removing a signature would be as good as forging one.
- A signature that is **absent is not an error**, since signing is optional. Unsigned
  containers open normally and the tool says plainly that nothing proves their origin.
  Use `-require-signature` when you want that to be refused.
- What gets signed includes **who it was for**. Without that, a recipient could
  re-encrypt your still-signed message to somebody else, who would then see a valid
  signature from you on a message you never sent them.

---

## Everything you can do with it

Every feature is a flag, and nothing hides behind an interactive menu. That is on
purpose: a tool you cannot script is a tool you cannot put in a nightly backup job.

### If you would rather click than type

```sh
noisecrypt gui
```

Your browser opens on a page served by the binary itself. Encrypt a file, decrypt one,
turn one into video and get it back, generate an identity. No installation, no account,
no configuration.

Three things worth knowing about it. It listens only on your own machine, on a port
chosen fresh each time, and is not reachable from your network, let alone the internet.
Nothing on the page is loaded from anywhere else, because a page that fetches a font
tells whoever serves that font you are using this tool. And it holds a one-time password
you never have to see: the address the binary opens already carries it, so a page you
happen to have open in another tab cannot reach in and ask this one to decrypt something.

The page can do everything the tool does, with four exceptions it names in its own
footer rather than leaving you to discover: `simulate`, the Argon2id work factor, the
video encoder settings, and feeding a passphrase from a file or the environment.

One button on that page is worth pointing at: **what will it cost**. Turning a file into
video takes as long as the video is long, and on the toughest channel a single mebibyte
is minutes of footage. The interface will tell you the number of frames and the running
time before you commit to it, rather than after.

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

| Profile | Use it when | Data per frame | A 40 MiB file becomes |
|---|---|---|---|
| `archive` | Nothing will re-encode the video: a hard drive, a USB stick, cloud storage, a torrent | 26 393 B | **54 seconds** |
| `social-hd` | A platform will chew on it, and you can download at 426p or better | 567 B | **41 minutes** |
| `social` | A platform will chew on it, and you want it readable even from the very bottom of the quality ladder | 123 B | **3 hours 9 minutes** |

The gap is dramatic and it is the central trade of the whole tool: **toughness is bought
with time.** `archive` assumes nobody will touch the file and packs data tightly. The two
`social` profiles assume the worst and spend most of each frame on redundancy.

Between the two, `social-hd` is the one to reach for, and the YouTube test settled it:
both profiles recovered every rendition, so the extra toughness bought nothing there and
cost four and a half times the duration. Keep `social` for a platform whose behaviour you
have not measured.

The floors below come from a local simulation of a platform's quality ladder, and they
are worth reading alongside the real test rather than instead of it. The simulation said
15 px cells would fail at 320p; YouTube does not produce a 320p rendition, so that
weakness never came up. It is still real, and it is why `social` exists.

| Cell size | Lowest quality that still decodes | Pixels per cell there |
|---|---|---|
| 30 px (`social`) | 256p | 4.0 |
| 15 px (`social-hd`) | 426p | 3.3 |
| 12 px | 640p | 4.0 |
| 10 px | 640p | 3.3 |

There is a genuinely odd result hiding in that table. 15 px cells **fail** at 320p and
then **succeed** again at 256p: lower quality, better outcome. The reason is that cells
have to land on whole pixels once they get small. At 320p a 15 px cell becomes 2.5
pixels and straddles a boundary; at 256p it becomes exactly 2 and lines up.

The useful rule, then, is not about resolution but about pixels per cell. Above roughly
six, a cell has interior pixels to average and survives sloppy boundaries. Below about
three, it does not, and alignment decides everything.

Pick with `-profile`, and use the same one at both ends:

```sh
noisecrypt encode -in big.zip -out big.mp4 -profile social-hd
noisecrypt decode -in big.mp4 -out big.zip -profile social-hd
```

Those numbers are calculated from the actual error-correcting layout rather than written
down beside it, so they cannot drift out of date when the code changes.

### Test the toughness yourself

```sh
noisecrypt simulate -profile social
```

This encodes a payload, re-compresses the video at a range of qualities and resolutions,
and reports which combinations still decode. It exists so that nothing here has to be
taken on faith. Run it on your own machine and see for yourself.

Add `-heights` to walk down a platform's quality ladder, which is where the interesting
failures live:

```sh
noisecrypt simulate -profile social-hd -heights 1920,1280,854,640,426
```

And if you want to tune your own geometry, override it before committing to it:

```sh
noisecrypt simulate -profile social -cell 12 -heights 1920,854,426
```

Candidates report themselves as unverified on every line, because a number you have not
measured is a guess wearing a number's clothes.

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

1. **A second platform.** Both social profiles have now been through YouTube and back.
   Every other platform remains an assumption, and the `social-hd` simulation shows it
   has a real weak point that YouTube simply never exercises.
2. **A graphical interface** for Linux, macOS and Windows, for the times a command line is
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

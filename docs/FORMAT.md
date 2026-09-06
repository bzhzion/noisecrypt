# NoiseCrypt container format, version 1

This document specifies the on-disk `.ncry` container. It is normative: an
independent implementation that follows it interoperates with this one.

The video layers (modulation, erasure coding, frame geometry) are not specified here
and are not implemented yet. They will sit *above* this format, carrying the byte
stream below unchanged.

All integers are big endian. All offsets are in bytes.

## Layers

```
  file bytes  +  name, mtime, size
        |
        v
  [ inner payload ]        NCPL, section 2. Confidential: this is plaintext to
        |                  the AEAD and never appears in the clear.
        v
  [ encrypted stream ]     NCR1, section 3. Cleartext header plus sealed chunks.
        |
        v
  [ .ncry file ]           Exactly the encrypted stream, byte for byte.
```

The ordering is compress, then encrypt, then (in a future version) erasure-code, then
modulate. Each step is only correct in that position:

- Compressing after encrypting gains nothing; ciphertext is incompressible.
- Encrypting after erasure coding destroys the correction, since parity computed over
  plaintext does not survive a cipher.

## 1. Notation

| Term | Meaning |
|---|---|
| `u8`, `u16`, `u32`, `u64` | Unsigned big-endian integers of that width |
| `b[n]` | Exactly `n` bytes |
| AEAD | XChaCha20-Poly1305, 24-byte nonce, 16-byte tag |
| KDF | HKDF-SHA-512 |

## 2. Inner payload (`NCPL`)

Produced before encryption, consumed after decryption. Never visible in a sealed
container.

| Offset | Size | Field |
|---|---|---|
| 0 | `b[4]` | Magic, ASCII `NCPL` |
| 4 | `u8` | Version, `1` |
| 5 | `u8` | Compression: `0` none, `1` gzip |
| 6 | `u8` | Flags. Bit 0 set means a signature block follows the body. All other bits reserved and must be `0` |
| 7 | `u16` | Name length `L`, at most 1024 |
| 9 | `b[L]` | File name, UTF-8 |
| 9+L | `u64` | Modification time, seconds since the Unix epoch, UTC |
| 17+L | `u64` | Original size, before compression |
| 25+L | `u64` | Body length `B` |
| 33+L | `b[B]` | Body, compressed if the compression byte says so |
| 33+L+B | `b[3200]` | *If bit 0 of flags is set:* the signer's public identity |
| 3233+L+B | `b[3373]` | *If bit 0 of flags is set:* the signature block |

Rules a conforming implementation must follow:

- **The stored name is a base name.** Any directory component is stripped on write
  *and* re-stripped on read. A member called `../../.ssh/authorized_keys` is a working
  exploit against any extractor that trusts what it is given.
- **Compression is kept only if it wins.** If the gzip body is not smaller than the
  input, store the input and set the compression byte to `0`. Already-compressed
  input grows under gzip, and on this channel wasted bytes are wasted minutes.
- **Decompression is bounded by the declared original size.** Reading an unbounded
  gzip stream is a decompression bomb.
- **The recovered length must equal the declared original size.** A mismatch is an
  error, not a warning.

### 2.1 Signatures

Signing is optional. Bit 0 of the flags byte says whether the two blocks at the end are
present; when it is clear, they are absent entirely and cost nothing.

```
signed_region = the payload from offset 0 up to and including the body,
                that is everything before the signer identity

msg = "noisecrypt/v1 payload signature"
    ‖ signer_public_identity(3200)
    ‖ recipient_fingerprint(32)
    ‖ signed_region

signature = Ed25519.Sign(signer_ed25519_secret, msg)                  // 64 bytes
          ‖ ML-DSA-65.Sign(signer_mldsa_secret, msg, ctx, hedged)     // 3309 bytes

ctx = "noisecrypt/v1 payload signature"
```

Both signatures are always produced and **both must verify**. There is deliberately no
mode that accepts one of the two: a verifier that would settle for the classical
signature alone has discarded the reason for having the other.

Three parts of `msg` are load-bearing and each closes a specific attack.

**The domain separator.** ML-DSA takes a context string natively; Ed25519 has no such
parameter, so the separator is also prepended to the message. Without it, an Ed25519
signature made here could be replayed as a signature made by any other protocol using
the same key over the same bytes.

**The signer's own identity.** A signed payload carries the signer's public identity so
the verifier knows whose key to check, and that identity holds four keys: two for
signing and two for encryption. Only the signing pair takes part in verification, so
without this term the *encryption* keys inside the claimed identity are covered by
nothing. An attacker can swap them, keep the signature valid, and a verifier composing
a reply from the identity in the container would encrypt it to the attacker. This was
found by bit-flipping every byte of a signed payload, not by reading the code.

**The recipient's fingerprint.** Without it the construction has a hole known as
surreptitious forwarding. Alice signs a payload and encrypts it to Bob; Bob decrypts,
keeps the still-valid signed payload, and re-encrypts it to Charlie, who sees a payload
signed by Alice and addressed to him. Binding the fingerprint makes Charlie's
verification fail. In passphrase mode the fingerprint is 32 zero bytes: there is no
recipient, and therefore no recipient to redirect the container towards.

Reader obligations specific to signatures:

1. **A signature that is present must verify.** A failure is fatal, never a warning.
   If a broken signature were tolerated, stripping one would be as effective as forging
   one.
2. **A signature that is absent is not an error**, because signing is optional.
   Requiring that a container *be* signed is the caller's decision, since only the
   caller knows whether it asked for one.
3. **Reject unknown flag bits** rather than ignoring them.
4. A payload whose flags claim a signature but which is too short to hold one is
   malformed, not unsigned.

## 3. Encrypted stream (`NCR1`)

```
  [ header ]  [ chunk 0 ]  [ chunk 1 ]  …  [ chunk n, final ]
```

### 3.1 Common header prefix

| Offset | Size | Field |
|---|---|---|
| 0 | `b[4]` | Magic, ASCII `NCR1` |
| 4 | `u8` | Format version, `1` |
| 5 | `u8` | Mode: `1` passphrase, `2` hybrid |
| 6 | `u8` | Suite: `1` XChaCha20-Poly1305 with HKDF-SHA-512 |
| 7 | `u8` | Reserved flags, must be `0` |
| 8 | `u32` | Chunk size, plaintext bytes per chunk |
| 12 | `b[19]` | Nonce prefix, random per container |

The common prefix is 31 bytes.

A reader must reject a version, mode or suite it does not implement rather than
guessing. A reader must reject a chunk size outside `[1024, 16777216]`.

### 3.2 Passphrase mode, total header 56 bytes

| Offset | Size | Field |
|---|---|---|
| 31 | `b[16]` | Argon2id salt |
| 47 | `u32` | Argon2id passes |
| 51 | `u32` | Argon2id memory, KiB |
| 55 | `u8` | Argon2id lanes |

```
master = Argon2id(passphrase, salt, passes, memory, lanes, 32)
```

**A reader must bound the cost parameters before using them.** They arrive from the
container, so they are attacker controlled, and they are read before any
authentication has happened; there is no way to verify them first, because verifying
requires the key they produce. This implementation refuses more than 2 GiB of memory
or more than 16 passes.

This is a denial-of-service bound, not a security bound. It was not theoretical: a
single flipped bit in the pass count asked for sixteen million passes and turned a
half-second test suite into a three-minute one.

Defaults when writing: 3 passes, 128 MiB, 4 lanes.

### 3.3 Hybrid mode, total header 1183 bytes

| Offset | Size | Field |
|---|---|---|
| 31 | `b[32]` | Recipient fingerprint, SHA-256 of the public identity |
| 63 | `b[32]` | Ephemeral X25519 public key |
| 95 | `b[1088]` | ML-KEM-768 ciphertext |

An identity carries four public keys, two for encryption and two for signing, one
classical and one post-quantum in each pair. Encryption keys cannot sign, so signatures
required their own pair rather than reusing the existing one.

A public identity is `X25519 ‖ ML-KEM-768 encapsulation ‖ Ed25519 ‖ ML-DSA-65`, that is
32 + 1184 + 32 + 1952 = **3200 bytes**, rendered as `noisecrypt-public-v1:` followed by
unpadded base64url, which comes to 4288 characters.

A private identity is `X25519 scalar ‖ ML-KEM-768 seed ‖ Ed25519 seed ‖ ML-DSA-65 seed`,
that is 32 + 64 + 32 + 32 = **160 bytes**, rendered with the `noisecrypt-secret-v1:`
prefix. Seeds rather than expanded keys: an ML-DSA-65 private key is 4032 bytes
unpacked and 32 as a seed, and expansion is deterministic.

Every component has a size fixed by its algorithm, so identities are fixed width with
no length prefixes. That is also what makes a layout mismatch fail loudly: an identity
from a different layout cannot be the right length, so it is rejected outright rather
than misparsed.

The public half is always **recomputed** from
the secret on load, never read from the stored blob, so a tampered key file cannot
make a receiver advertise a public key that does not match its secret.

```
transcript = SHA-512( "noisecrypt/v1 hybrid transcript"
                    ‖ recipient_public_identity
                    ‖ ephemeral_x25519_public
                    ‖ mlkem_ciphertext )

master = HKDF-SHA-512( ikm  = x25519_shared ‖ mlkem_shared,
                       salt = transcript,
                       info = "noisecrypt/v1 hybrid master",
                       len  = 32 )
```

Both shared secrets go into the input keying material. HKDF-Extract over the pair is a
secure PRF as long as either half is unpredictable, which is precisely the hybrid
property: breaking X25519 alone, or ML-KEM alone, does not reveal the key.

The transcript in the salt binds the recipient identity, the ephemeral key and the
ciphertext, so an attacker who can rewrite header bytes cannot steer two parties onto
different keys.

The fingerprint is a routing hint, not a security check. It lets a receiver holding
several identities pick one without trial decapsulation, at the cost of telling an
observer which identity a container is addressed to. Forging it does not help an
attacker: ML-KEM decapsulation of a ciphertext meant for someone else returns a
deterministic pseudo-random key rather than an error, so the failure surfaces at the
AEAD, which is exactly where it should.

### 3.4 Stream key

```
header_hash = SHA-256(encoded header)
stream_key  = HKDF-SHA-512( ikm  = master,
                            salt = header_hash,
                            info = "noisecrypt/v1 stream key",
                            len  = 32 )
```

Deriving from the header hash is what makes header tampering fatal. Flip a bit in the
chunk size, the mode byte or the KEM ciphertext and the derived key changes, so every
chunk fails to authenticate instead of being decrypted under parameters an attacker
chose.

### 3.5 Chunks

Each chunk is:

| Size | Field |
|---|---|
| `u32` | Sealed length, plaintext length plus 16 |
| `b[len]` | AEAD output |

For chunk index `i`, with `final` true only for the last chunk:

```
nonce = nonce_prefix(19) ‖ u32(i) ‖ (final ? 0x01 : 0x00)      // 24 bytes
ad    = header_hash(32)  ‖ u32(i) ‖ (final ? 0x01 : 0x00)      // 37 bytes
```

The index and the final flag appear in both the nonce and the associated data. That is
redundant on purpose: an implementation that ignored the associated data entirely
would still get reordering and truncation resistance from the nonce alone.

Properties this yields, and the attack each one closes:

| Property | Attack closed |
|---|---|
| Index in the nonce | Reordering or dropping a chunk from the middle |
| Final flag in the nonce | Truncating the stream, whose remaining chunks are individually valid |
| Header hash in the associated data | Swapping the header for another |
| Random nonce prefix per container | Nonce reuse across two containers under the same passphrase |

An empty plaintext still produces exactly one chunk, the final one. Without it there
would be no end-of-stream marker and a reader could not distinguish an empty message
from a message truncated to nothing.

Bytes after the final chunk are an error. A reader must not ignore them.

### 3.6 Reader obligations

A conforming reader must:

1. Reject an unknown version, mode or suite instead of guessing.
2. Reject a non-zero reserved flags byte.
3. Bound the chunk size, the Argon2id memory and the Argon2id pass count before
   allocating or computing anything from them.
4. Reject a declared chunk length that exceeds the header's chunk size plus 16, or
   that exceeds the bytes actually present.
5. Report wrong passphrase, wrong recipient, and tampered ciphertext as the same
   failure, so a caller cannot use the distinction as an oracle.
6. Reject any data following the end-of-stream chunk.
7. Sanitise the stored file name on read, not only on write.

Items 3, 4 and 7 are the ones an implementation written from this document alone is
most likely to skip, because each looks like defensive paperwork until it is the bug.

## 4. Test vectors

### 4.1 The reference identity

Every vector below derives from one fixed private identity, so an independent
implementation can reproduce all of them from this string alone with nothing carried
over from whoever generated them.

A private identity is 160 raw bytes, so the first 160 bytes of the counting pattern
`00 01 02 ... 9f` form a valid one:

```
seed (hex, 160 bytes)
  000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f
  202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f
  404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f
  606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f
  808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f
```

This is a published fixture, not a secret. Anything sealed to it is public.

### 4.2 Derived values

| Value | Expected |
|---|---|
| Private identity length | 160 bytes |
| Public identity length | 3200 bytes |
| Public token length | 4288 characters, after the `noisecrypt-public-v1:` prefix and base64url |
| Signature block length | 3373 bytes |
| Public identity fingerprint | `efabdb74cfb3b19b0bbd96e685bb3e31e5f6b68a8e15b7018ec98d0db2f5007a` |

The fingerprint is SHA-256 over the 3200 serialised public bytes. If an implementation
derives the same identity from the seed and gets a different fingerprint, the layout of
`§3.3` has been read differently, most likely the key order.

### 4.3 What is deliberately not pinned

**There is no fixed ciphertext vector, and this is not an omission.**

Sealing is randomised: every container has a fresh nonce prefix and, in passphrase mode,
a fresh Argon2id salt. Producing a reproducible ciphertext would mean fixing those, and
a format that can be made deterministic on demand invites an implementation that ships
that way by accident. Nonce reuse under a stream cipher leaks the exclusive-or of two
plaintexts, so this is a bad trade for the convenience of a static test file.

The interoperability guarantee is therefore stated in the direction that can be checked
without weakening anything: **a container produced by any conforming implementation must
open**. That is what the reference implementation tests, in both modes, signed and
unsigned, in `internal/crypt/vectors_test.go`.

Anyone writing a second implementation can pin the same way: derive the identity above,
confirm the fingerprint, then seal and open against the reference binary in both
directions.

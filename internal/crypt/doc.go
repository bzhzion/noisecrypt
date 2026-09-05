// Package crypt implements NoiseCrypt's encryption layer.
//
// Design notes, because the choices here are the point of the project.
//
// # Post-quantum, not "quantum"
//
// There is no such thing as quantum encryption in a command line tool. What is
// achievable, and what this package does, is post-quantum resistance: resisting an
// adversary who records ciphertext today and runs a cryptographically relevant
// quantum computer against it later. That threat is real for a format whose whole
// purpose is to park data in a place it will sit for years.
//
// Two things follow. Symmetric cryptography is already fine: Grover's algorithm
// costs a square root, so a 256-bit key still leaves a 128-bit margin. Key exchange
// is not fine: X25519 falls to Shor. So the passphrase mode needs no post-quantum
// work beyond a wide enough key, and the recipient mode needs a lattice KEM.
//
// # Hybrid, never lattice-only
//
// Recipient mode runs X25519 and ML-KEM-768 (FIPS 203) side by side and feeds both
// shared secrets into a single HKDF transcript. The result stays secure as long as
// *either* primitive holds. ML-KEM is young and its security estimates still move;
// X25519 is old and falls to a quantum computer. Betting on one alone means betting
// on either a fresh lattice assumption or on quantum computers never arriving.
// Neither is a bet worth taking for archival data.
//
// The transcript binds both public keys and the ciphertext, so an attacker who can
// tamper with one half of the exchange cannot steer the derived key.
//
// # STREAM, not one tag over the whole file
//
// The obvious construction, a single AEAD over the entire plaintext, is wrong for
// this channel. NoiseCrypt exists to push data through lossy transports; a format
// where one damaged byte makes the whole payload undecryptable throws away the
// erasure coding that sits above it. This package therefore uses the STREAM
// construction: the plaintext is split into chunks, each sealed separately, with
// the chunk index and an end-of-stream flag bound into the nonce and the associated
// data. That yields the three properties a naive chunked format loses: chunks
// cannot be reordered, cannot be dropped from the end (truncation), and cannot be
// spliced in from another message.
//
// # What is deliberately not protected
//
// The header is authenticated but not confidential; it has to be readable to
// bootstrap decryption. It reveals that a NoiseCrypt container exists, its version,
// its cipher suite, its chunk size, and, in recipient mode, a fingerprint of the
// recipient identity. It does not reveal the file name, the file size, or the
// modification time: those live in the encrypted payload, written by the container
// layer.
//
// This is not steganography and offers no deniability. A video full of visual noise
// is the least discreet object one can upload. Encryption protects the content, not
// the fact that content exists.
package crypt

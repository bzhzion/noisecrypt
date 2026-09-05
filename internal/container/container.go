// Package container wraps a file and its metadata into the byte stream that the
// encryption layer seals.
//
// The single rule this package exists to enforce: file metadata travels *inside*
// the ciphertext. A container's cleartext header says how to decrypt, never what
// was encrypted. Leaking "invoices-2026.zip, 4.2 MB, modified last Tuesday" in the
// clear would undo most of the point of encrypting the payload at all, and it is
// the mistake almost every home-grown archive format makes, because putting the
// name in the header is so much easier to implement.
package container

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bzhzion/noisecrypt/internal/crypt"
)

// Magic identifies the inner payload format. It is never visible in a sealed
// container; it only ever exists as plaintext inside the AEAD.
const Magic = "NCPL"

// Version is the payload format version.
const Version = 1

// Compression identifies how the file bytes were compressed.
type Compression uint8

const (
	// CompressionNone stores the bytes as they are.
	CompressionNone Compression = 0

	// CompressionGzip stores them gzip-compressed.
	CompressionGzip Compression = 1
)

const (
	// MaxNameLength bounds the stored file name. Long enough for any real name,
	// short enough that a hostile payload cannot make a decoder allocate freely.
	MaxNameLength = 1024

	// MaxPayloadSize bounds what a single container may declare, so a corrupted
	// or hostile length field cannot turn into a multi-gigabyte allocation.
	MaxPayloadSize = 1 << 40 // 1 TiB
)

var (
	// ErrMalformedPayload is returned when the decrypted bytes are not a valid
	// payload. Reaching this after a successful AEAD open means the sender
	// produced garbage, not that an attacker tampered with anything.
	ErrMalformedPayload = errors.New("container: malformed payload")

	// ErrUnsupportedPayload is returned for a well formed payload announcing a
	// version or compression this build does not implement.
	ErrUnsupportedPayload = errors.New("container: unsupported payload")
)

// flagSigned marks a payload that carries a signature block.
const flagSigned = 1 << 0

// PackOptions configures packing. Signing is opt-in: leave Signer nil and the payload
// carries no signature block at all, which is the default and costs nothing.
type PackOptions struct {
	Metadata

	// Signer, when set, signs the payload. The signature and the signer's public
	// identity travel inside the ciphertext, so only the recipient learns who sent
	// the container. Putting them outside would tell every observer.
	Signer *crypt.PrivateIdentity

	// Recipient is the fingerprint bound into the signature, closing surreptitious
	// forwarding: a recipient cannot re-encrypt a still-valid signed payload to a
	// third party and have it verify there. Zero in passphrase mode, where there is
	// no recipient to redirect the container towards.
	Recipient [crypt.FingerprintSize]byte
}

// Metadata describes the encoded file. Every field here is confidential.
type Metadata struct {
	// Name is the base name of the original file, with any directory component
	// stripped. Storing a path would let a hostile container write outside the
	// output directory on extraction.
	Name string

	// ModTime is the original modification time, truncated to whole seconds.
	ModTime time.Time

	// OriginalSize is the length of the file before compression.
	OriginalSize uint64

	// Compression records how Payload was compressed.
	Compression Compression
}

// FallbackName is used whenever the stored name cannot be made safe.
const FallbackName = "payload.bin"

// windowsReserved lists the device names Windows resolves before ever touching the
// filesystem. The reservation applies with any extension too, so CON.txt is just as
// reserved as CON.
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitiseName reduces an arbitrary path to a safe base name.
//
// Path traversal through an archive member name is old and still effective: a member
// called ../../.ssh/authorized_keys is a working exploit against any extractor that
// trusts the stored name. This runs at both ends, on write so we never store a path,
// and on read so a container produced by another implementation cannot smuggle one in.
//
// # Why the Windows rules apply everywhere
//
// The three checks below are Windows quirks, and they are applied unconditionally
// rather than behind a build tag, because the machine that writes a container is not
// the machine that opens it. Sanitising for the reader's platform means sanitising for
// the worst one.
//
// All three were verified by experiment rather than assumed, and all three previously
// made this tool report a successful extraction while doing something else:
//
//   - A reserved device name (NUL, CON, COM1...) opens the device. The write returns
//     no error and reports the full byte count, and the file on disk is zero bytes.
//     Silent, total data loss, announced as a success.
//   - A colon opens an alternate data stream: "report.txt:hidden" writes the payload
//     into a stream attached to report.txt, which Explorer and dir show as an empty
//     file. The data exists and is invisible.
//   - A trailing dot or space is silently stripped by the filesystem, so the file
//     lands under a different name than the one the tool just printed.
func SanitiseName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)

	// Windows strips trailing dots and spaces, so strip them here instead and keep
	// the reported name equal to the name on disk.
	name = strings.TrimRight(name, ". ")

	switch name {
	case "", ".", "..", "/":
		return FallbackName
	}

	// Reject the separators and the characters Windows forbids outright. The colon
	// is the important one: it selects an alternate data stream rather than failing.
	if strings.ContainsAny(name, "\x00/:*?\"<>|") {
		return FallbackName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return FallbackName
		}
	}
	if !utf8.ValidString(name) {
		return FallbackName
	}

	// A reserved device name is reserved with any extension, so test the stem.
	stem, _, _ := strings.Cut(name, ".")
	if windowsReserved[strings.ToLower(stem)] {
		return FallbackName
	}

	if len(name) > MaxNameLength {
		name = name[:MaxNameLength]
	}
	return name
}

// Pack builds the plaintext payload for a file.
//
// Compression runs before encryption, which is the only correct order: ciphertext
// is incompressible by construction, so compressing afterwards would gain nothing.
// The reverse trade is real and worth naming, because compressing before encrypting
// leaks information through the ciphertext length. For a format whose length is
// already visible as a video duration measured in minutes, that leak is
// unavoidable, and the bandwidth saved is the difference between a usable tool and
// an unusable one.
func Pack(meta Metadata, data []byte) ([]byte, error) {
	return PackWith(PackOptions{Metadata: meta}, data)
}

// PackWith builds the plaintext payload, optionally signed.
//
// The signature covers everything before it: the magic, the version, the compression
// byte, the name, the timestamps and the body. Signing only the body would leave the
// metadata swappable, so a container could keep a valid signature while claiming a
// different file name.
func PackWith(opts PackOptions, data []byte) ([]byte, error) {
	meta := opts.Metadata
	meta.Name = SanitiseName(meta.Name)
	meta.OriginalSize = uint64(len(data))

	body := data
	compression := CompressionNone

	if meta.Compression != CompressionNone {
		compressed, err := gzipCompress(data)
		if err != nil {
			return nil, err
		}
		// Only keep the compressed form if it actually won. Already-compressed
		// input (a zip, a jpeg, a video) grows slightly under gzip, and on this
		// channel every wasted byte is wasted seconds of video.
		if len(compressed) < len(data) {
			body = compressed
			compression = CompressionGzip
		}
	}
	meta.Compression = compression

	nameBytes := []byte(meta.Name)
	modTime := meta.ModTime.UTC().Unix()

	var flags byte
	if opts.Signer != nil {
		flags |= flagSigned
	}

	out := make([]byte, 0, 4+1+1+1+2+len(nameBytes)+8+8+8+len(body))
	out = append(out, Magic...)
	out = append(out, Version, byte(compression), flags)
	out = binary.BigEndian.AppendUint16(out, uint16(len(nameBytes)))
	out = append(out, nameBytes...)
	out = binary.BigEndian.AppendUint64(out, uint64(modTime))
	out = binary.BigEndian.AppendUint64(out, meta.OriginalSize)
	out = binary.BigEndian.AppendUint64(out, uint64(len(body)))
	out = append(out, body...)

	if opts.Signer == nil {
		return out, nil
	}

	sig, err := crypt.Sign(out, opts.Recipient, opts.Signer)
	if err != nil {
		return nil, err
	}
	out = append(out, opts.Signer.Public.Bytes()...)
	out = append(out, sig...)
	return out, nil
}

// Opened is the result of unpacking: the metadata, the file, and who signed it.
type Opened struct {
	Metadata
	Data []byte

	// Signer is set only when the payload carried a valid signature. A nil Signer
	// means the container was unsigned, which is not an error: signing is optional.
	// A container whose signature is *present and invalid* never reaches here at all.
	Signer *crypt.PublicIdentity
}

// Unpack is the inverse of Pack, ignoring any signature.
//
// Kept for callers that genuinely do not care who sent something. Anything that
// reports provenance to a user should call Open instead, so that a present-but-broken
// signature cannot be silently discarded.
func Unpack(payload []byte) (Metadata, []byte, error) {
	o, err := Open(payload, [crypt.FingerprintSize]byte{})
	if err != nil {
		return Metadata{}, nil, err
	}
	return o.Metadata, o.Data, nil
}

// Open unpacks a payload and verifies its signature if it has one.
//
// The policy, which is the part worth being deliberate about: a signature that is
// present must verify, and a failure here is fatal. A signature that is absent is not
// an error, because signing is optional. Those two rules together are what make the
// feature worth anything; if a broken signature were a warning, stripping it would be
// as good as forging it.
//
// Requiring that a container *be* signed is the caller's decision, not this function's,
// because only the caller knows whether it asked for one.
func Open(payload []byte, recipient [crypt.FingerprintSize]byte) (Opened, error) {
	const fixedPrefix = 4 + 1 + 1 + 1 + 2
	if len(payload) < fixedPrefix {
		return Opened{}, fmt.Errorf("%w: %d bytes is shorter than the prefix", ErrMalformedPayload, len(payload))
	}
	if string(payload[:4]) != Magic {
		return Opened{}, fmt.Errorf("%w: bad magic", ErrMalformedPayload)
	}
	if payload[4] != Version {
		return Opened{}, fmt.Errorf("%w: payload version %d, this build reads %d",
			ErrUnsupportedPayload, payload[4], Version)
	}

	compression := Compression(payload[5])
	switch compression {
	case CompressionNone, CompressionGzip:
	default:
		return Opened{}, fmt.Errorf("%w: compression %d", ErrUnsupportedPayload, compression)
	}

	flags := payload[6]
	if flags&^byte(flagSigned) != 0 {
		return Opened{}, fmt.Errorf("%w: unknown payload flags %#x", ErrUnsupportedPayload, flags)
	}

	// Split the signature block off before parsing anything else, so the rest of the
	// parser sees exactly the bytes the signature covers.
	signed := payload
	var signer *crypt.PublicIdentity
	if flags&flagSigned != 0 {
		const blockSize = crypt.PublicIdentitySize + crypt.SignatureSize
		if len(payload) < fixedPrefix+blockSize {
			return Opened{}, fmt.Errorf("%w: payload claims a signature but is too short", ErrMalformedPayload)
		}
		cut := len(payload) - blockSize
		signed = payload[:cut]
		block := payload[cut:]

		id, err := crypt.ParsePublicIdentityBytes(block[:crypt.PublicIdentitySize])
		if err != nil {
			return Opened{}, err
		}
		if err := crypt.Verify(signed, recipient, id, block[crypt.PublicIdentitySize:]); err != nil {
			return Opened{}, err
		}
		signer = &id
	}

	payload = signed
	nameLen := int(binary.BigEndian.Uint16(payload[7:9]))
	if nameLen > MaxNameLength {
		return Opened{}, fmt.Errorf("%w: name length %d exceeds %d", ErrMalformedPayload, nameLen, MaxNameLength)
	}
	rest := payload[fixedPrefix:]
	if len(rest) < nameLen+24 {
		return Opened{}, fmt.Errorf("%w: truncated metadata block", ErrMalformedPayload)
	}

	meta := Metadata{
		Name:        SanitiseName(string(rest[:nameLen])),
		Compression: compression,
	}
	rest = rest[nameLen:]

	meta.ModTime = time.Unix(int64(binary.BigEndian.Uint64(rest[0:8])), 0).UTC()
	meta.OriginalSize = binary.BigEndian.Uint64(rest[8:16])
	bodyLen := binary.BigEndian.Uint64(rest[16:24])
	rest = rest[24:]

	if bodyLen > MaxPayloadSize || meta.OriginalSize > MaxPayloadSize {
		return Opened{}, fmt.Errorf("%w: declared size exceeds the %d byte ceiling", ErrMalformedPayload, uint64(MaxPayloadSize))
	}
	if uint64(len(rest)) != bodyLen {
		return Opened{}, fmt.Errorf("%w: body declares %d bytes, %d are present",
			ErrMalformedPayload, bodyLen, len(rest))
	}

	data := rest
	if compression == CompressionGzip {
		var err error
		// Bound the decompressed output at the size the metadata declared. Without
		// it, a small hostile payload expands without limit on decode, which is the
		// classic decompression bomb.
		data, err = gzipDecompress(rest, meta.OriginalSize)
		if err != nil {
			return Opened{}, err
		}
	}

	if uint64(len(data)) != meta.OriginalSize {
		return Opened{}, fmt.Errorf("%w: recovered %d bytes, metadata declares %d",
			ErrMalformedPayload, len(data), meta.OriginalSize)
	}

	return Opened{Metadata: meta, Data: data, Signer: signer}, nil
}

func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("container: initialising gzip: %w", err)
	}
	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("container: compressing: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("container: finalising gzip: %w", err)
	}
	return buf.Bytes(), nil
}

func gzipDecompress(data []byte, limit uint64) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip header: %v", ErrMalformedPayload, err)
	}
	defer zr.Close()

	// Read one byte past the limit so an oversized payload is detected rather than
	// silently truncated to the declared size.
	out, err := io.ReadAll(io.LimitReader(zr, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip body: %v", ErrMalformedPayload, err)
	}
	if uint64(len(out)) > limit {
		return nil, fmt.Errorf("%w: compressed body expands past the declared %d bytes", ErrMalformedPayload, limit)
	}
	return out, nil
}

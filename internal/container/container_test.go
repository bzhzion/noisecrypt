package container

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bzhzion/noisecrypt/internal/crypt"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		mode Compression
	}{
		{"empty", nil, CompressionGzip},
		{"tiny", []byte("x"), CompressionGzip},
		{"compressible", bytes.Repeat([]byte("abcdefgh"), 5000), CompressionGzip},
		{"stored", bytes.Repeat([]byte("abcdefgh"), 5000), CompressionNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := Metadata{
				Name:        "report.pdf",
				ModTime:     time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC),
				Compression: tc.mode,
			}

			packed, err := Pack(meta, tc.data)
			if err != nil {
				t.Fatalf("Pack: %v", err)
			}

			got, data, err := Unpack(packed)
			if err != nil {
				t.Fatalf("Unpack: %v", err)
			}
			if !bytes.Equal(data, tc.data) {
				t.Fatalf("data mismatch: got %d bytes, want %d", len(data), len(tc.data))
			}
			if got.Name != meta.Name {
				t.Fatalf("name: got %q, want %q", got.Name, meta.Name)
			}
			if !got.ModTime.Equal(meta.ModTime) {
				t.Fatalf("mod time: got %v, want %v", got.ModTime, meta.ModTime)
			}
			if got.OriginalSize != uint64(len(tc.data)) {
				t.Fatalf("original size: got %d, want %d", got.OriginalSize, len(tc.data))
			}
		})
	}
}

// TestIncompressibleInputIsStored checks the "only keep the win" rule. Random bytes
// grow under gzip, and on this channel a few wasted percent is a few wasted minutes
// of video.
func TestIncompressibleInputIsStored(t *testing.T) {
	data := make([]byte, 64*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("reading randomness: %v", err)
	}

	packed, err := Pack(Metadata{Name: "noise.bin", Compression: CompressionGzip}, data)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	meta, got, err := Unpack(packed)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if meta.Compression != CompressionNone {
		t.Fatalf("incompressible input was stored gzipped anyway")
	}
	if !bytes.Equal(got, data) {
		t.Fatal("data mismatch")
	}
}

// TestMetadataNeverAppearsInCleartext is the property this whole package exists
// for. If the file name shows up in the sealed bytes, the design has failed no
// matter how good the cipher is.
func TestMetadataNeverAppearsInCleartext(t *testing.T) {
	const secretName = "salaires-2026-confidentiel.xlsx"

	packed, err := Pack(Metadata{
		Name:        secretName,
		ModTime:     time.Now(),
		Compression: CompressionGzip,
	}, []byte("some spreadsheet bytes"))
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	sealed, err := crypt.Seal(packed, crypt.SealOptions{
		Passphrase: []byte("passphrase"),
		KDF:        crypt.KDFParams{Time: 1, Memory: 8, Lanes: 1},
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if bytes.Contains(sealed, []byte(secretName)) {
		t.Fatal("the file name appears verbatim in the sealed container")
	}
	if bytes.Contains(sealed, []byte(Magic)) {
		t.Fatal("the inner payload magic appears in the sealed container")
	}
	if bytes.Contains(sealed, []byte("xlsx")) {
		t.Fatal("the file extension appears in the sealed container")
	}
}

// TestSanitiseNameRejectsWindowsTraps covers three behaviours found by experiment
// during the 2026-09-05 audit, each of which made the tool report a successful
// extraction while doing something else entirely.
//
// They are checked on every platform on purpose: the machine that writes a container
// is not the machine that opens it, so the reader has to sanitise for the worst
// target, not for its own.
func TestSanitiseNameRejectsWindowsTraps(t *testing.T) {
	cases := map[string]string{
		// Reserved device names: the write succeeds, reports the full byte count,
		// and leaves zero bytes on disk.
		"NUL":     "device name, silently discards the payload",
		"nul":     "device names are case insensitive",
		"CON":     "device name",
		"COM1":    "device name",
		"LPT9":    "device name",
		"NUL.txt": "reserved with any extension too",

		// Alternate data stream: the payload lands in a hidden stream and the
		// visible file shows as empty.
		"report.txt:hidden": "alternate data stream",
		":stream":           "alternate data stream",

		// Characters Windows forbids, which fail or behave oddly rather than
		// producing the named file.
		"a<b":    "forbidden character",
		"a|b":    "forbidden character",
		"a\"b":   "forbidden character",
		"a\x1fb": "control character",
	}

	for in, why := range cases {
		if got := SanitiseName(in); got != FallbackName {
			t.Errorf("SanitiseName(%q) = %q, expected the fallback (%s)", in, got, why)
		}
	}

	// Trailing dots and spaces are stripped rather than rejected, so the name the
	// tool prints is the name that lands on disk.
	for in, want := range map[string]string{
		"trailing.":    "trailing",
		"trailing...":  "trailing",
		"trailing ":    "trailing",
		"trailing . .": "trailing",
	} {
		if got := SanitiseName(in); got != want {
			t.Errorf("SanitiseName(%q) = %q, want %q", in, got, want)
		}
	}

	// And the ordinary cases must still survive all of that.
	for _, ok := range []string{"report.pdf", "notes.md", "étoile.txt", "console.log", "communication.txt"} {
		if got := SanitiseName(ok); got != ok {
			t.Errorf("SanitiseName(%q) = %q, a legitimate name was rejected", ok, got)
		}
	}
}

func TestSanitiseName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":                   "report.pdf",
		"/etc/passwd":                  "passwd",
		"C:\\Windows\\System32\\a.dll": "a.dll",
		"../../.ssh/authorized_keys":   "authorized_keys",
		"..":                           "payload.bin",
		".":                            "payload.bin",
		"":                             "payload.bin",
		"   ":                          "payload.bin",
		"with\x00null":                 "payload.bin",
		"étoile.txt":                   "étoile.txt",
	}

	for in, want := range cases {
		if got := SanitiseName(in); got != want {
			t.Errorf("SanitiseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPathTraversalCannotSurviveAContainer covers the case where a hostile
// producer writes a traversal path directly into the payload bytes, bypassing
// Pack. Unpack must sanitise on the way out too.
func TestPathTraversalCannotSurviveAContainer(t *testing.T) {
	packed, err := Pack(Metadata{Name: "safe.txt", Compression: CompressionNone}, []byte("data"))
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	// Overwrite the stored name in place with the same-length traversal path.
	hostile := []byte("../../evil.sh")
	if len(hostile) != len("safe.txt") {
		// Rebuild the payload with a matching length instead of assuming.
		packed, err = Pack(Metadata{Name: strings.Repeat("a", len(hostile)), Compression: CompressionNone}, []byte("data"))
		if err != nil {
			t.Fatalf("Pack: %v", err)
		}
	}
	// The stored name starts right after the fixed prefix: magic, version,
	// compression, flags, name length.
	const nameOffset = 4 + 1 + 1 + 1 + 2
	copy(packed[nameOffset:nameOffset+len(hostile)], hostile)

	meta, _, err := Unpack(packed)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if strings.ContainsAny(meta.Name, `/\`) || strings.Contains(meta.Name, "..") {
		t.Fatalf("Unpack returned an unsanitised name: %q", meta.Name)
	}
}

func TestUnpackRejectsMalformedPayloads(t *testing.T) {
	good, err := Pack(Metadata{Name: "a.txt", Compression: CompressionNone}, []byte("hello"))
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	t.Run("empty", func(t *testing.T) {
		if _, _, err := Unpack(nil); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("expected ErrMalformedPayload, got %v", err)
		}
	})

	t.Run("bad magic", func(t *testing.T) {
		b := bytes.Clone(good)
		b[0] = 'X'
		if _, _, err := Unpack(b); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("expected ErrMalformedPayload, got %v", err)
		}
	})

	t.Run("unknown version", func(t *testing.T) {
		b := bytes.Clone(good)
		b[4] = 99
		if _, _, err := Unpack(b); !errors.Is(err, ErrUnsupportedPayload) {
			t.Fatalf("expected ErrUnsupportedPayload, got %v", err)
		}
	})

	t.Run("unknown compression", func(t *testing.T) {
		b := bytes.Clone(good)
		b[5] = 99
		if _, _, err := Unpack(b); !errors.Is(err, ErrUnsupportedPayload) {
			t.Fatalf("expected ErrUnsupportedPayload, got %v", err)
		}
	})

	t.Run("truncated body", func(t *testing.T) {
		if _, _, err := Unpack(good[:len(good)-2]); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("expected ErrMalformedPayload, got %v", err)
		}
	})
}

// TestDecompressionBombIsRefused builds a payload whose gzip body expands far past
// the size its own metadata declares. The limit reader must stop it.
func TestDecompressionBombIsRefused(t *testing.T) {
	bomb := bytes.Repeat([]byte{0}, 8*1024*1024)

	packed, err := Pack(Metadata{Name: "bomb.bin", Compression: CompressionGzip}, bomb)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	// Rewrite the declared original size to something tiny, leaving the real
	// eight-megabyte gzip body in place.
	// magic, version, compression, flags, name length, then the name, then the
	// modification time. The declared original size follows.
	const fixedPrefix = 4 + 1 + 1 + 1 + 2
	off := fixedPrefix + len("bomb.bin") + 8
	for i := range 8 {
		packed[off+i] = 0
	}
	packed[off+7] = 16 // declares 16 bytes

	if _, _, err := Unpack(packed); !errors.Is(err, ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

// TestFullStackRoundTrip exercises the two layers together, which is how they are
// actually used: pack, seal, open, unpack.
func TestFullStackRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("the quick brown fox "), 10000)
	meta := Metadata{
		Name:        "fox.txt",
		ModTime:     time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
		Compression: CompressionGzip,
	}

	id, err := crypt.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	packed, err := Pack(meta, data)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	sealed, err := crypt.Seal(packed, crypt.SealOptions{Recipient: &id.Public})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, err := crypt.Open(sealed, crypt.OpenOptions{Identity: id})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gotMeta, gotData, err := Unpack(opened)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	if !bytes.Equal(gotData, data) {
		t.Fatal("data did not survive the full stack")
	}
	if gotMeta.Name != meta.Name || !gotMeta.ModTime.Equal(meta.ModTime) {
		t.Fatalf("metadata did not survive: %+v", gotMeta)
	}
	if gotMeta.Compression != CompressionGzip {
		t.Fatal("highly compressible input was not compressed")
	}
}

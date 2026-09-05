package cli

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"github.com/bzhzion/noisecrypt/internal/codec"
	"github.com/bzhzion/noisecrypt/internal/container"
	"github.com/bzhzion/noisecrypt/internal/crypt"
	"github.com/bzhzion/noisecrypt/internal/profile"
	"github.com/bzhzion/noisecrypt/internal/video"
)

func runEncode(env *Env, args []string) error {
	fs := newFlagSet(env, "encode", "-in FILE [-out FILE] [-profile NAME] [-to IDENTITY | passphrase flags]")
	in := fs.String("in", "", "file to encode (required)")
	out := fs.String("out", "", "video to write (default: the input name with .mp4 appended)")
	profileName := fs.String("profile", profile.Archive.Name, "channel profile: "+profileList())
	to := fs.String("to", "", "recipient public identity, or a file containing one; omit to seal with a passphrase")
	noCompress := fs.Bool("no-compress", false, "skip compression (use for already-compressed input)")
	crf := fs.Int("crf", 18, "x264 quality, lower is better")
	preset := fs.String("preset", "medium", "x264 speed and size tradeoff")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	kdfTime := fs.Uint("kdf-time", uint(crypt.DefaultArgonTime), "Argon2id passes (passphrase mode)")
	kdfMemory := fs.Uint("kdf-memory", uint(crypt.DefaultArgonMemory), "Argon2id memory in KiB (passphrase mode)")
	kdfLanes := fs.Uint("kdf-lanes", uint(crypt.DefaultArgonLanes), "Argon2id lanes (passphrase mode)")
	pass := &passphraseSource{confirm: true}
	pass.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		fs.Usage()
		return errors.New("-in is required")
	}

	p, err := profile.Lookup(*profileName)
	if err != nil {
		return err
	}
	c, err := codec.New(p)
	if err != nil {
		return err
	}
	tools, err := video.Find()
	if err != nil {
		return err
	}

	kdf, err := kdfFromFlags(*kdfTime, *kdfMemory, *kdfLanes)
	if err != nil {
		return err
	}

	sealed, plainSize, err := sealFile(env, *in, *to, *noCompress, pass, kdf)
	if err != nil {
		return err
	}

	target := *out
	if target == "" {
		target = *in + ".mp4"
	}
	if !*force {
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("%s already exists; pass -force to overwrite it", target)
		}
	}

	// Say what it will cost before spending it, not after. A user who learns at the
	// end that their file became three hours of video has already waited.
	e := p.Estimate(plainSize, int64(len(sealed)))
	fmt.Fprintf(env.Stdout, "Encoding %s (%s sealed) with the %s profile.\n",
		*in, humanBytes(int64(len(sealed))), p.Name)
	fmt.Fprintf(env.Stdout, "  %d frames, %s of video at %dx%d, %d fps\n",
		e.Frames, humanDuration(e.Duration), p.Width, p.Height, p.FPS)

	return encodeToVideo(env, c, tools, sealed, target, *crf, *preset)
}

// encodeToVideo bridges the push-shaped encoder and the pull-shaped muxer.
//
// codec.EncodeTo calls a function per frame; video.Write asks for the next frame.
// Neither can be reshaped without giving up what it is for, so a small goroutine and
// an unbuffered channel connect them. The buffer is deliberately tiny: the point of
// streaming is not to hold frames, and one 1920 by 1080 frame in flight is two
// megabytes while a hundred would be two hundred.
func encodeToVideo(env *Env, c *codec.Codec, tools video.Tools, sealed []byte, target string, crf int, preset string) error {
	p := c.Profile()

	type item struct {
		img *image.Gray
		err error
	}
	ch := make(chan item, 2)

	go func() {
		defer close(ch)
		err := c.EncodeTo(sealed, func(_ int, img *image.Gray) error {
			ch <- item{img: img}
			return nil
		})
		if err != nil {
			ch <- item{err: err}
		}
	}()

	start := time.Now()
	written := 0
	err := video.Write(context.Background(), tools, video.WriteOptions{
		Path: target, Width: p.Width, Height: p.Height, FPS: p.FPS, CRF: crf, Preset: preset,
	}, func() (*image.Gray, error) {
		it, ok := <-ch
		if !ok {
			return nil, nil
		}
		if it.err != nil {
			return nil, it.err
		}
		written++
		return it.img, nil
	})
	if err != nil {
		return err
	}

	info, statErr := os.Stat(target)
	size := int64(0)
	if statErr == nil {
		size = info.Size()
	}
	fmt.Fprintf(env.Stdout, "Wrote %s: %d frames, %s, in %s.\n",
		target, written, humanBytes(size), time.Since(start).Round(time.Second))
	return nil
}

func runDecode(env *Env, args []string) error {
	fs := newFlagSet(env, "decode", "-in VIDEO [-out FILE] [-profile NAME] [-identity FILE | passphrase flags]")
	in := fs.String("in", "", "video to decode (required)")
	out := fs.String("out", "", "file to write (default: the name stored inside the container)")
	profileName := fs.String("profile", profile.Archive.Name, "channel profile used to encode: "+profileList())
	identity := fs.String("identity", "", "private identity, or a file containing one")
	force := fs.Bool("force", false, "overwrite the output file if it already exists")
	pass := &passphraseSource{}
	pass.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		fs.Usage()
		return errors.New("-in is required")
	}

	p, err := profile.Lookup(*profileName)
	if err != nil {
		return err
	}
	c, err := codec.New(p)
	if err != nil {
		return err
	}
	tools, err := video.Find()
	if err != nil {
		return err
	}

	d := c.NewDecoder()
	info, err := video.Read(context.Background(), tools, *in, func(img *image.Gray) error {
		d.Add(img)
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "Read %s: %dx%d, %d frames, %d unreadable.\n",
		*in, info.Width, info.Height, d.Seen, d.Unreadable)
	if info.Width != p.Width || info.Height != p.Height {
		// Worth saying out loud. A mismatch is normal after a platform round trip
		// and is exactly the case the geometry layer exists for, but it is also
		// what a wrong -profile looks like, and the two are worth telling apart
		// before spending time on a decode that cannot work.
		fmt.Fprintf(env.Stdout, "  (encoded at %dx%d, so this has been rescaled; check -profile if the decode fails)\n",
			p.Width, p.Height)
	}

	sealed, err := d.Finish()
	if err != nil {
		return err
	}

	payload, meta, err := openSealed(env, sealed, *identity, pass)
	if err != nil {
		return err
	}

	target := *out
	if target == "" {
		target = meta.Name
	}
	if err := writeOutput(target, payload, *force, 0o600); err != nil {
		return err
	}
	if !meta.ModTime.IsZero() {
		_ = os.Chtimes(target, meta.ModTime, meta.ModTime)
	}

	fmt.Fprintf(env.Stdout, "Recovered %s (%s).\n", target, humanBytes(int64(len(payload))))
	return nil
}

// runSimulate is the command that turns the profile table from folklore into
// measurement.
//
// It encodes a real payload, re-encodes the result at a range of qualities, and
// reports which ones still decode. That is the question every parameter in a channel
// profile is an answer to, and until now nobody could ask it: the tuning constant
// this project inherited was chosen once, by hand, and no user could check it.
func runSimulate(env *Env, args []string) error {
	fs := newFlagSet(env, "simulate", "[-in FILE] [-profile NAME] [geometry overrides]")
	in := fs.String("in", "", "file to use as the payload (default: a synthetic one block payload)")
	profileName := fs.String("profile", "", "profile to test (default: all of them)")
	crfList := fs.String("crf", "20,26,30,34,38,42", "comma separated x264 qualities to re-encode at")
	keep := fs.String("keep", "", "directory to leave the produced videos in, for inspection")

	// Geometry overrides, so a candidate can be measured before it is registered as
	// a profile. Tuning a channel by editing a constant, rebuilding and eyeballing
	// the result is how unverifiable magic numbers get into a codec; this makes the
	// measurement the cheap step and the commit the consequence.
	cell := fs.Int("cell", 0, "override the cell size in pixels")
	levels := fs.Int("levels", 0, "override the amplitude levels per cell (2, 4 or 16)")
	intraParity := fs.Float64("intra-parity", 0, "override the intra-frame parity ratio, 0 to 1")
	interParity := fs.Int("inter-parity", 0, "override the number of parity frames per block")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tools, err := video.Find()
	if err != nil {
		return err
	}

	crfs, err := parseInts(*crfList)
	if err != nil {
		return fmt.Errorf("-crf: %w", err)
	}

	profiles := profile.All()
	if *profileName != "" {
		p, err := profile.Lookup(*profileName)
		if err != nil {
			return err
		}
		profiles = []profile.Profile{p}
	}

	overridden := *cell != 0 || *levels != 0 || *intraParity != 0 || *interParity != 0
	if overridden {
		if len(profiles) != 1 {
			return errors.New("geometry overrides need a single -profile to start from")
		}
		p := profiles[0]
		if *cell != 0 {
			p.CellSize = *cell
		}
		if *levels != 0 {
			p.Levels = *levels
		}
		if *intraParity != 0 {
			p.IntraParityRatio = *intraParity
		}
		if *interParity != 0 {
			p.InterParity = *interParity
		}
		// A candidate is by definition not the measured profile it came from, and
		// saying so on every line is the whole point of the flag.
		p.Name = p.Name + "-candidate"
		p.Verified = false
		if err := p.Validate(); err != nil {
			return err
		}
		profiles = []profile.Profile{p}
	}

	dir := *keep
	if dir == "" {
		dir, err = os.MkdirTemp("", "noisecrypt-simulate-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	for _, p := range profiles {
		c, err := codec.New(p)
		if err != nil {
			return err
		}

		payload, err := simulationPayload(*in, c)
		if err != nil {
			return err
		}

		fmt.Fprintf(env.Stdout, "\n%s: %s payload, %d frames at %dx%d\n",
			p.Name, humanBytes(int64(len(payload))), c.FrameCount(len(payload)), p.Width, p.Height)

		// The reference encode. Everything after this is a second pass on top of it,
		// which is what an ingest pipeline does to a video it is given.
		master := filepath.Join(dir, p.Name+"-master.mp4")
		if err := encodeQuietly(c, tools, payload, master, 16, "medium"); err != nil {
			return err
		}

		for _, crf := range crfs {
			out := filepath.Join(dir, fmt.Sprintf("%s-crf%d.mp4", p.Name, crf))
			if err := recompress(tools, master, out, p, crf); err != nil {
				return err
			}

			d := c.NewDecoder()
			if _, err := video.Read(context.Background(), tools, out, func(img *image.Gray) error {
				d.Add(img)
				return nil
			}); err != nil {
				return err
			}

			size := int64(0)
			if fi, err := os.Stat(out); err == nil {
				size = fi.Size()
			}

			got, decErr := d.Finish()
			switch {
			case decErr != nil:
				fmt.Fprintf(env.Stdout, "  CRF %-3d %-9s FAILED   %d of %d frames unreadable: %v\n",
					crf, humanBytes(size), d.Unreadable, d.Seen, decErr)
			case len(got) != len(payload) || string(got) != string(payload):
				fmt.Fprintf(env.Stdout, "  CRF %-3d %-9s WRONG    decoded but the bytes differ\n", crf, humanBytes(size))
			default:
				fmt.Fprintf(env.Stdout, "  CRF %-3d %-9s ok       %d of %d frames unreadable\n",
					crf, humanBytes(size), d.Unreadable, d.Seen)
			}
		}
	}

	if *keep != "" {
		fmt.Fprintf(env.Stdout, "\nVideos left in %s\n", *keep)
	}
	fmt.Fprintln(env.Stdout, "\nThese are local re-encodes, not a platform. A platform also rescales,")
	fmt.Fprintln(env.Stdout, "changes the frame rate and crops; treat this as a lower bound on the damage.")
	return nil
}

func simulationPayload(path string, c *codec.Codec) ([]byte, error) {
	if path != "" {
		return readInput(path)
	}
	// One block, filled with a pattern that compresses badly, so the simulation is
	// not accidentally measuring an easy case.
	n := c.Layout().BlockPayload()
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*31 + i/251)
	}
	return out, nil
}

func encodeQuietly(c *codec.Codec, tools video.Tools, payload []byte, path string, crf int, preset string) error {
	p := c.Profile()

	type item struct {
		img *image.Gray
		err error
	}
	ch := make(chan item, 2)
	go func() {
		defer close(ch)
		if err := c.EncodeTo(payload, func(_ int, img *image.Gray) error {
			ch <- item{img: img}
			return nil
		}); err != nil {
			ch <- item{err: err}
		}
	}()

	return video.Write(context.Background(), tools, video.WriteOptions{
		Path: path, Width: p.Width, Height: p.Height, FPS: p.FPS, CRF: crf, Preset: preset,
	}, func() (*image.Gray, error) {
		it, ok := <-ch
		if !ok {
			return nil, nil
		}
		return it.img, it.err
	})
}

// recompress reads a video and writes it out again at a lower quality, which is the
// cheapest honest stand-in for an ingest pipeline.
func recompress(tools video.Tools, src, dst string, p profile.Profile, crf int) error {
	var frames []*image.Gray
	if _, err := video.Read(context.Background(), tools, src, func(img *image.Gray) error {
		frames = append(frames, img)
		return nil
	}); err != nil {
		return err
	}

	next := 0
	return video.Write(context.Background(), tools, video.WriteOptions{
		Path: dst, Width: p.Width, Height: p.Height, FPS: p.FPS, CRF: crf, Preset: "veryfast",
	}, func() (*image.Gray, error) {
		if next >= len(frames) {
			return nil, nil
		}
		img := frames[next]
		next++
		return img, nil
	})
}

// sealFile packs and seals a file, returning the container and the plaintext size.
func sealFile(env *Env, path, to string, noCompress bool, pass *passphraseSource, kdf crypt.KDFParams) ([]byte, int64, error) {
	data, err := readInput(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}

	compression := container.CompressionGzip
	if noCompress {
		compression = container.CompressionNone
	}
	packed, err := container.Pack(container.Metadata{
		Name:        filepath.Base(path),
		ModTime:     info.ModTime(),
		Compression: compression,
	}, data)
	if err != nil {
		return nil, 0, err
	}

	opts := crypt.SealOptions{}
	if to != "" {
		recipient, err := resolveRecipient(to)
		if err != nil {
			return nil, 0, err
		}
		opts.Recipient = &recipient
	} else {
		p, err := pass.resolve(env, "Passphrase: ")
		if err != nil {
			return nil, 0, err
		}
		opts.Passphrase = p
		opts.KDF = kdf
	}

	sealed, err := crypt.Seal(packed, opts)
	if err != nil {
		return nil, 0, err
	}
	return sealed, int64(len(data)), nil
}

// openSealed opens a container and unpacks it.
func openSealed(env *Env, sealed []byte, identity string, pass *passphraseSource) ([]byte, container.Metadata, error) {
	header, _, err := crypt.ParseHeader(sealed)
	if err != nil {
		return nil, container.Metadata{}, err
	}

	var opts crypt.OpenOptions
	switch header.Mode {
	case crypt.ModeHybrid:
		if identity == "" {
			return nil, container.Metadata{}, errors.New("this container is sealed to an identity; pass -identity")
		}
		id, err := resolveIdentity(identity)
		if err != nil {
			return nil, container.Metadata{}, err
		}
		opts.Identity = id
	default:
		p, err := pass.resolve(env, "Passphrase: ")
		if err != nil {
			return nil, container.Metadata{}, err
		}
		opts.Passphrase = p
	}

	payload, err := crypt.Open(sealed, opts)
	if err != nil {
		return nil, container.Metadata{}, err
	}
	meta, data, err := container.Unpack(payload)
	if err != nil {
		return nil, container.Metadata{}, err
	}
	return data, meta, nil
}

func profileList() string {
	names := profile.Names()
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, part := range splitComma(s) {
		var v int
		if _, err := fmt.Sscanf(part, "%d", &v); err != nil {
			return nil, fmt.Errorf("%q is not a number", part)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("no values given")
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		if r != ' ' {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

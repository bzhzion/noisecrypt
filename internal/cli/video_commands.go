package cli

import (
	"bytes"
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
	signWith := fs.String("sign", "", "sign the container with this private identity, or a file containing one")
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

	sealed, plainSize, err := sealFile(env, *in, sealOptions{
		to: *to, signWith: *signWith, noCompress: *noCompress, kdf: kdf,
	}, pass)
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
	from := fs.String("from", "", "require that the container be signed by this public identity")
	requireSig := fs.Bool("require-signature", false, "refuse a container that carries no signature")
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

	opened, err := openSealed(env, sealed, openOptions{
		identity: *identity, from: *from, requireSignature: *requireSig,
	}, pass)
	if err != nil {
		return err
	}

	target := *out
	if target == "" {
		target = opened.Name
	}
	if err := writeOutput(target, opened.Data, *force, 0o600); err != nil {
		return err
	}
	if !opened.ModTime.IsZero() {
		_ = os.Chtimes(target, opened.ModTime, opened.ModTime)
	}

	fmt.Fprintf(env.Stdout, "Recovered %s (%s).\n", target, humanBytes(int64(len(opened.Data))))
	reportSigner(env, opened)
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
	heightList := fs.String("heights", "", "comma separated heights to also downscale to, mimicking a platform's rendition ladder (default: the profile's own height only)")
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

	var heights []int
	if *heightList != "" {
		if heights, err = parseInts(*heightList); err != nil {
			return fmt.Errorf("-heights: %w", err)
		}
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

		ladder := heights
		if len(ladder) == 0 {
			ladder = []int{p.Height}
		}

		fmt.Fprintf(env.Stdout, "\n%s: %s payload, %d frames at %dx%d, %d px cells, %d levels\n",
			p.Name, humanBytes(int64(len(payload))), c.FrameCount(len(payload)),
			p.Width, p.Height, p.CellSize, p.Levels)

		// The reference encode. Everything after this is a second pass on top of it,
		// which is what an ingest pipeline does to a video it is given.
		master := filepath.Join(dir, p.Name+"-master.mp4")
		if err := encodeQuietly(c, tools, payload, master, 16, "medium"); err != nil {
			return err
		}

		for _, height := range ladder {
			for _, crf := range crfs {
				out := filepath.Join(dir, fmt.Sprintf("%s-h%d-crf%d.mp4", p.Name, height, crf))
				if err := recompress(tools, master, out, p, crf, height); err != nil {
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

				// How many pixels a cell has left is the number that decides
				// everything, so print it rather than making the reader derive it.
				cell := float64(p.CellSize) * float64(height) / float64(p.Height)
				label := fmt.Sprintf("%dp CRF %-3d cell %4.1f px", height, crf, cell)

				got, decErr := d.Finish()
				switch {
				case decErr != nil:
					fmt.Fprintf(env.Stdout, "  %-28s %-9s FAILED   %d of %d frames unreadable\n",
						label, humanBytes(size), d.Unreadable, d.Seen)
				case len(got) != len(payload) || string(got) != string(payload):
					fmt.Fprintf(env.Stdout, "  %-28s %-9s WRONG    decoded but the bytes differ\n",
						label, humanBytes(size))
				default:
					fmt.Fprintf(env.Stdout, "  %-28s %-9s ok       %d of %d frames unreadable\n",
						label, humanBytes(size), d.Unreadable, d.Seen)
				}
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

// recompress reads a video and writes it out again at a lower quality, and optionally
// at a lower resolution.
//
// The rescaling matters more than the compression, and its absence was a real hole in
// this command. A platform does not just squeeze a video, it produces a ladder of
// renditions and a viewer usually receives one well below what was uploaded. Measuring
// only the compression answered the easier half of the question while the output text
// admitted the other half was untested. Now the resolution floor is measurable without
// uploading anything.
func recompress(tools video.Tools, src, dst string, p profile.Profile, crf, height int) error {
	var frames []*image.Gray
	if _, err := video.Read(context.Background(), tools, src, func(img *image.Gray) error {
		frames = append(frames, img)
		return nil
	}); err != nil {
		return err
	}

	width, outHeight := p.Width, p.Height
	if height > 0 && height != p.Height {
		// Keep the aspect ratio and force an even width, which H.264 requires.
		width = p.Width * height / p.Height
		width -= width % 2
		outHeight = height - height%2
		frames = resizeFrames(frames, width, outHeight)
	}

	next := 0
	return video.Write(context.Background(), tools, video.WriteOptions{
		Path: dst, Width: width, Height: outHeight, FPS: p.FPS, CRF: crf, Preset: "veryfast",
	}, func() (*image.Gray, error) {
		if next >= len(frames) {
			return nil, nil
		}
		img := frames[next]
		next++
		return img, nil
	})
}

// resizeFrames downscales by area averaging, which is what a sane resampler does and
// what makes a cell's value the mean of the pixels it used to occupy.
func resizeFrames(frames []*image.Gray, w, h int) []*image.Gray {
	out := make([]*image.Gray, len(frames))
	for i, src := range frames {
		sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
		dst := image.NewGray(image.Rect(0, 0, w, h))
		for y := range h {
			y0, y1 := y*sh/h, max(y*sh/h+1, (y+1)*sh/h)
			for x := range w {
				x0, x1 := x*sw/w, max(x*sw/w+1, (x+1)*sw/w)
				sum, n := 0, 0
				for yy := y0; yy < min(y1, sh); yy++ {
					row := src.Pix[yy*src.Stride:]
					for xx := x0; xx < min(x1, sw); xx++ {
						sum += int(row[xx])
						n++
					}
				}
				if n > 0 {
					dst.Pix[y*dst.Stride+x] = uint8(sum / n)
				}
			}
		}
		out[i] = dst
	}
	return out
}

// sealOptions gathers everything the two sealing commands share.
type sealOptions struct {
	to         string
	signWith   string
	noCompress bool
	kdf        crypt.KDFParams
}

// sealFile packs and seals a file, returning the container and the plaintext size.
func sealFile(env *Env, path string, o sealOptions, pass *passphraseSource) ([]byte, int64, error) {
	data, err := readInput(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}

	compression := container.CompressionGzip
	if o.noCompress {
		compression = container.CompressionNone
	}

	packOpts := container.PackOptions{
		Metadata: container.Metadata{
			Name:        filepath.Base(path),
			ModTime:     info.ModTime(),
			Compression: compression,
		},
	}

	cryptOpts := crypt.SealOptions{}
	if o.to != "" {
		recipient, err := resolveRecipient(o.to)
		if err != nil {
			return nil, 0, err
		}
		cryptOpts.Recipient = &recipient
		// The recipient goes into the signature so the container cannot be re-aimed
		// at a third party with its signature intact.
		packOpts.Recipient = recipient.Fingerprint()
	} else {
		p, err := pass.resolve(env, "Passphrase: ")
		if err != nil {
			return nil, 0, err
		}
		cryptOpts.Passphrase = p
		cryptOpts.KDF = o.kdf
	}

	if o.signWith != "" {
		signer, err := resolveIdentity(o.signWith)
		if err != nil {
			return nil, 0, err
		}
		packOpts.Signer = signer
	}

	packed, err := container.PackWith(packOpts, data)
	if err != nil {
		return nil, 0, err
	}

	sealed, err := crypt.Seal(packed, cryptOpts)
	if err != nil {
		return nil, 0, err
	}
	return sealed, int64(len(data)), nil
}

// openOptions gathers what the two opening commands share.
type openOptions struct {
	identity string

	// from, when set, is the public identity the container must be signed by.
	from string

	// requireSignature refuses an unsigned container. Without it an unsigned
	// container opens normally, because signing is optional; with it, the absence of
	// a signature is an error, which is the only way "signed" means anything.
	requireSignature bool
}

// openSealed opens a container, unpacks it, and reports who signed it.
func openSealed(env *Env, sealed []byte, o openOptions, pass *passphraseSource) (container.Opened, error) {
	header, _, err := crypt.ParseHeader(sealed)
	if err != nil {
		return container.Opened{}, err
	}

	var opts crypt.OpenOptions
	var recipient [crypt.FingerprintSize]byte

	switch header.Mode {
	case crypt.ModeHybrid:
		if o.identity == "" {
			return container.Opened{}, errors.New("this container is sealed to an identity; pass -identity")
		}
		id, err := resolveIdentity(o.identity)
		if err != nil {
			return container.Opened{}, err
		}
		opts.Identity = id
		recipient = id.Public.Fingerprint()
	default:
		p, err := pass.resolve(env, "Passphrase: ")
		if err != nil {
			return container.Opened{}, err
		}
		opts.Passphrase = p
	}

	payload, err := crypt.Open(sealed, opts)
	if err != nil {
		return container.Opened{}, err
	}

	// Open verifies the signature if there is one, against this recipient.
	opened, err := container.Open(payload, recipient)
	if err != nil {
		return container.Opened{}, err
	}

	if err := checkSigner(opened, o); err != nil {
		return container.Opened{}, err
	}
	return opened, nil
}

// checkSigner applies the caller's expectations about provenance.
func checkSigner(opened container.Opened, o openOptions) error {
	if o.from != "" {
		want, err := resolveRecipient(o.from)
		if err != nil {
			return fmt.Errorf("-from: %w", err)
		}
		if opened.Signer == nil {
			return fmt.Errorf("%w: -from was given but this container is not signed", crypt.ErrWrongSigner)
		}
		if !bytes.Equal(opened.Signer.Bytes(), want.Bytes()) {
			return crypt.ErrWrongSigner
		}
		return nil
	}

	if o.requireSignature && opened.Signer == nil {
		return errors.New("this container is not signed, and -require-signature was given")
	}
	return nil
}

// reportSigner tells the user what the signature did or did not prove. Saying nothing
// when a container is unsigned would let the absence pass for a pass.
//
// The short fingerprint, not the full token: 4288 characters on every successful decode
// would bury the rest of the output and train people to skip it.
func reportSigner(env *Env, opened container.Opened) {
	if opened.Signer == nil {
		fmt.Fprintln(env.Stdout, "  Not signed: nothing here proves who produced this container.")
		return
	}
	fmt.Fprintf(env.Stdout, "  Signature verified, signed by %s\n", opened.Signer.Short())
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

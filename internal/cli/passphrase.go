package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// passphraseSource collects the mutually exclusive ways a caller can supply a
// passphrase, in decreasing order of safety.
type passphraseSource struct {
	// file reads the passphrase from a file, minus any trailing newline. This is
	// the option to use in a script.
	file string

	// env names an environment variable holding the passphrase. Safer than a flag
	// (it does not reach the process list on Linux) but still readable by anything
	// that can read /proc for the process, so it is documented as second best.
	env string

	// confirm asks for the passphrase twice. Used when sealing, where a typo would
	// otherwise produce a container nobody can open.
	confirm bool
}

func (s *passphraseSource) register(fs interface {
	StringVar(*string, string, string, string)
}) {
	fs.StringVar(&s.file, "passphrase-file", "", "read the passphrase from this file (recommended for scripts; '-' reads standard input)")
	fs.StringVar(&s.env, "passphrase-env", "", "read the passphrase from this environment variable")
}

// ErrNoPassphrase is returned when a passphrase is required and none could be read.
var ErrNoPassphrase = errors.New("no passphrase supplied")

// MinPassphraseLength is the floor enforced when sealing.
//
// The Argon2id cost does not save a short passphrase. Three passes over 128 MiB makes
// each guess expensive, but "a" is one guess: the work factor multiplies the cost of
// searching a keyspace, and a keyspace of a few hundred candidates stays trivial no
// matter what it is multiplied by. A tool that advertises post-quantum key exchange
// and then accepts a one-character passphrase without a word is not being honest
// about what protects the data.
//
// The floor applies when sealing only. Opening never enforces it, because a container
// made elsewhere, or made before this check existed, must still open.
const MinPassphraseLength = 8

// resolve returns the passphrase, prompting on the terminal as a last resort.
//
// There is deliberately no --passphrase flag. A passphrase on the command line
// lands in the shell history file and in the output of `ps` for every other user on
// the machine, and once it is there it is there for good. Offering the flag at all
// would mean most users reach for it.
func (s *passphraseSource) resolve(env *Env, prompt string) ([]byte, error) {
	set := 0
	if s.file != "" {
		set++
	}
	if s.env != "" {
		set++
	}
	if set > 1 {
		return nil, errors.New("use either -passphrase-file or -passphrase-env, not both")
	}

	switch {
	case s.file == "-":
		b, err := readAll(env.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading the passphrase from standard input: %w", err)
		}
		return s.accept(trimNewline(b))

	case s.file != "":
		b, err := os.ReadFile(s.file)
		if err != nil {
			return nil, fmt.Errorf("reading the passphrase file: %w", err)
		}
		return s.accept(trimNewline(b))

	case s.env != "":
		v, ok := os.LookupEnv(s.env)
		if !ok {
			return nil, fmt.Errorf("environment variable %s is not set", s.env)
		}
		return s.accept([]byte(v))
	}

	if env.ReadPassphrase == nil {
		return nil, ErrNoPassphrase
	}
	first, err := env.ReadPassphrase(prompt)
	if err != nil {
		return nil, err
	}
	if _, err := s.accept(first); err != nil {
		return nil, err
	}
	if !s.confirm {
		return first, nil
	}

	again, err := env.ReadPassphrase("Confirm passphrase: ")
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, again) {
		return nil, errors.New("the two passphrases do not match")
	}
	return first, nil
}

// accept applies the length floor. The floor is tied to s.confirm, which is set only
// when sealing: opening must accept whatever the container was made with.
func (s *passphraseSource) accept(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrNoPassphrase
	}
	if s.confirm && len(b) < MinPassphraseLength {
		return nil, fmt.Errorf("passphrase is %d bytes, the minimum for sealing is %d; "+
			"the Argon2id cost multiplies the price of searching a keyspace, it does not create one",
			len(b), MinPassphraseLength)
	}
	return b, nil
}

func readAll(r any) ([]byte, error) {
	type reader interface{ Read([]byte) (int, error) }
	rd, ok := r.(reader)
	if !ok {
		return nil, errors.New("no readable standard input")
	}
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := rd.Read(tmp)
		buf.Write(tmp[:n])
		if err != nil {
			if strings.Contains(err.Error(), "EOF") {
				return buf.Bytes(), nil
			}
			return nil, err
		}
		// A passphrase longer than this is a file, not a passphrase.
		if buf.Len() > 1<<20 {
			return nil, errors.New("passphrase input exceeds 1 MiB")
		}
	}
}

// readPassphraseFromTerminal reads without echo. It fails rather than falling back
// to an echoing read: silently printing a passphrase to a shared terminal, or into
// a CI log, is worse than refusing.
func readPassphraseFromTerminal(prompt string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("standard input is not a terminal; use -passphrase-file or -passphrase-env")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading the passphrase: %w", err)
	}
	return b, nil
}

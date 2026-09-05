// Package cli implements the NoiseCrypt command line.
//
// Everything the tool can do is reachable from flags and arguments, with no
// interactive prompt in the way. That is a deliberate reaction to prior art in this
// space, where the only entry point was a numbered menu: it made the tool
// impossible to script, impossible to put in a cron job, impossible to run over a
// batch of files, and impossible to test end to end. An interactive mode is a
// convenience layer that may be added on top later; it is not the interface.
//
// The one exception is passphrase entry, which reads from the terminal without
// echo when no other source is given. A passphrase on a command line ends up in the
// shell history and in the process list, where any other user on the machine can
// read it.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Version is overwritten at build time with the value of the git tag. It is never
// committed with a real version number: the version lives in the tags.
var Version = "dev"

type command struct {
	name    string
	summary string
	run     func(env *Env, args []string) error
}

// Env carries the process environment a command needs, so tests can drive the CLI
// without touching the real stdout, stderr or terminal.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	// ReadPassphrase reads a passphrase without echoing it. Tests replace it.
	ReadPassphrase func(prompt string) ([]byte, error)
}

// DefaultEnv returns the environment used by the real binary.
func DefaultEnv() *Env {
	return &Env{
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		Stdin:          os.Stdin,
		ReadPassphrase: readPassphraseFromTerminal,
	}
}

var commands = []command{
	{"encode", "encrypt a file and carry it as video", runEncode},
	{"decode", "recover a file from a video", runDecode},
	{"keygen", "generate a hybrid X25519 + ML-KEM-768 identity", runKeygen},
	{"seal", "encrypt a file into a .ncry container, without the video step", runSeal},
	{"open", "decrypt a .ncry container back to the original file", runOpen},
	{"estimate", "report the video cost of encoding a file, before encoding it", runEstimate},
	{"simulate", "measure which re-encoding qualities a profile survives", runSimulate},
	{"profiles", "list the available channel profiles", runProfiles},
	{"version", "print the version", runVersion},
}

// Run dispatches a command line. It returns the process exit code.
func Run(env *Env, args []string) int {
	if len(args) == 0 {
		usage(env.Stderr)
		return 2
	}

	name := args[0]
	if name == "-h" || name == "--help" || name == "help" {
		usage(env.Stdout)
		return 0
	}

	for _, c := range commands {
		if c.name != name {
			continue
		}
		if err := c.run(env, args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 2
			}
			fmt.Fprintf(env.Stderr, "noisecrypt %s: %v\n", name, err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(env.Stderr, "noisecrypt: unknown command %q\n\n", name)
	usage(env.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "noisecrypt %s\n\n", Version)
	fmt.Fprintln(w, "Post-quantum encrypted containers, on their way to becoming video.")
	fmt.Fprintln(w, "\nUsage:\n  noisecrypt <command> [flags]\n\nCommands:")

	width := 0
	for _, c := range commands {
		width = max(width, len(c.name))
	}
	for _, c := range commands {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.name, c.summary)
	}
	fmt.Fprintln(w, "\nRun 'noisecrypt <command> -h' for the flags of a command.")
}

// newFlagSet builds a flag set that reports errors through the command's error
// return rather than calling os.Exit, so Run stays testable.
func newFlagSet(env *Env, name, usageLine string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "Usage: noisecrypt %s %s\n\n", name, usageLine)
		fs.PrintDefaults()
	}
	return fs
}

func runVersion(env *Env, args []string) error {
	fs := newFlagSet(env, "version", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, Version)
	return nil
}

// humanBytes formats a byte count for a human reading a terminal. Binary units,
// because that is what the rest of the tool reasons in.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// humanDuration formats a number of seconds the way someone deciding whether to
// wait would want to read it.
func humanDuration(seconds float64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%.0f s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d min %02d s", int(seconds)/60, int(seconds)%60)
	default:
		h := int(seconds) / 3600
		m := (int(seconds) % 3600) / 60
		return fmt.Sprintf("%d h %02d min", h, m)
	}
}

func trimNewline(b []byte) []byte {
	return []byte(strings.TrimRight(string(b), "\r\n"))
}

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// run drives the whole command line and returns what it printed and its exit code.
func run(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = Run(&Env{Stdout: &out, Stderr: &errOut}, args)
	return out.String(), errOut.String(), code
}

// Help was reachable by three spellings and not by the fourth, and the missing one was
// `-help`: the form Go's own flag package prints, and therefore the one a Go user tries
// first. It exited 2 on "unknown command". A help flag that has to be guessed correctly
// is not help, so all of them are accepted and this says which.
func TestHelpAnswersToEverySpelling(t *testing.T) {
	for _, arg := range []string{"-h", "-help", "--help", "help"} {
		stdout, stderr, code := run(arg)
		if code != 0 {
			t.Errorf("%q exited %d, stderr: %s", arg, code, strings.TrimSpace(stderr))
		}
		if !strings.Contains(stdout, "Commands:") {
			t.Errorf("%q printed no command list", arg)
		}
	}
}

func TestVersionAnswersToEverySpelling(t *testing.T) {
	for _, arg := range []string{"-v", "-version", "--version", "version"} {
		stdout, stderr, code := run(arg)
		if code != 0 {
			t.Errorf("%q exited %d, stderr: %s", arg, code, strings.TrimSpace(stderr))
		}
		if strings.TrimSpace(stdout) != Version {
			t.Errorf("%q printed %q rather than the version", arg, strings.TrimSpace(stdout))
		}
	}
}

// The point of the help is to be a complete inventory. A command added to the dispatch
// table and not to the summary list would be a feature nobody can find, and nothing else
// would notice: the command still works, it is simply invisible.
func TestHelpListsEveryCommand(t *testing.T) {
	stdout, _, _ := run("-help")
	for _, c := range commands {
		if !strings.Contains(stdout, c.name) {
			t.Errorf("%q is dispatchable but absent from the help", c.name)
		}
		if c.summary == "" {
			t.Errorf("%q is listed with no summary", c.name)
		}
	}
}

// FFmpeg is the one prerequisite that makes commands fail, and it applies to three of
// the ten. A list of commands cannot show that, so the help says it.
func TestHelpNamesTheOnlyPrerequisite(t *testing.T) {
	stdout, _, _ := run("-help")
	if !strings.Contains(stdout, "FFmpeg") {
		t.Error("the help never mentions FFmpeg, so a user meets it as an error instead")
	}
	for _, needs := range []string{"encode", "decode", "simulate"} {
		if !strings.Contains(stdout, needs) {
			t.Errorf("the FFmpeg note does not name %q", needs)
		}
	}
}

// An unknown command still has to point somewhere useful, and it must not look like
// success to a script.
func TestUnknownCommandFailsAndExplains(t *testing.T) {
	_, stderr, code := run("encrpyt")
	if code == 0 {
		t.Error("a typo exited 0")
	}
	if !strings.Contains(stderr, "Commands:") {
		t.Error("an unknown command does not show what the commands are")
	}
}

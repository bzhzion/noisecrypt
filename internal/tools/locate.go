// Package tools finds external programs this tool calls but does not ship.
//
// Extracted from internal/video, where the search was written for FFmpeg and had its
// directory list hardcoded. A second caller needed the same search with a different list,
// and copying it would have meant two places to fix the day a package manager changes
// where it puts things.
//
// # Why nothing is bundled or downloaded
//
// FFmpeg and yt-dlp are found if they are installed, and their absence is reported rather
// than repaired. Bundling them would end the six-target build in one matrix, and fetching
// a binary at run time would turn the supply chain into an execution step on a tool whose
// argument is that it depends on nothing. So the tool asks the machine what it has.
package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNotInstalled says the program is absent, as opposed to present and broken. Callers
// use it to tell "install this" from "this failed".
var ErrNotInstalled = errors.New("not installed")

// NotFoundError carries where the search went, so a caller can render its own wording
// without this package guessing at it. Each caller already has a sentinel of its own and
// a phrasing that suits the program it is looking for.
type NotFoundError struct {
	Name  string
	Where string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %s: looked in PATH and %s", e.Name, ErrNotInstalled, e.Where)
}

// Is makes errors.Is(err, ErrNotInstalled) hold without the sentinel having to be wrapped
// into the message twice.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotInstalled }

// Places lists where to look for a program beyond PATH.
//
// Globs are separate from directories because the case that needs them is real rather
// than hypothetical: WinGet does not shim every package, so some binaries land under a
// directory whose name carries a version number. A fixed path cannot find those and never
// will.
type Places struct {
	Dirs  []string
	Globs []string
}

// Locate returns the full path to a program, searching PATH first.
func Locate(name string, p Places) (string, error) {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if found, err := exec.LookPath(name); err == nil {
		return found, nil
	}

	for _, d := range p.Dirs {
		if found, ok := ExecutableIn(d, name); ok {
			return found, nil
		}
	}
	for _, g := range p.Globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			continue
		}
		for _, d := range matches {
			if found, ok := ExecutableIn(d, name); ok {
				return found, nil
			}
		}
	}

	where := strings.Join(p.Dirs, ", ")
	if len(p.Globs) > 0 {
		if where != "" {
			where += ", "
		}
		where += strings.Join(p.Globs, ", ")
	}
	return "", &NotFoundError{Name: name, Where: where}
}

// ExecutableIn reports whether a directory holds a runnable file of that name.
func ExecutableIn(dir, name string) (string, bool) {
	if dir == "" {
		return "", false
	}
	full := filepath.Join(dir, name)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return "", false
	}
	// On Unix the execute bit is the difference between a program and a file that
	// happens to share its name. On Windows the mode carries nothing useful, so
	// existence is all there is to check.
	if runtime.GOOS != "windows" && info.Mode()&fs.FileMode(0o111) == 0 {
		return "", false
	}
	return full, true
}

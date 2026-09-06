//go:build !windows

package cli

// LaunchedByDoubleClick is always false away from Windows.
//
// Not an oversight. On Linux a binary is not something a file manager runs by default,
// and on macOS a double-clicked executable opens a Terminal that stays on screen with
// the output still in it, so the problem this exists to solve does not arise. Guessing
// on those platforms would mean opening a browser for someone who typed the command and
// wanted its usage.
func LaunchedByDoubleClick() bool { return false }

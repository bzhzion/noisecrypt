//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

// LaunchedByDoubleClick reports whether this process owns the console window it is
// printing into, which on Windows is what tells a double-click in Explorer apart from a
// command typed into a shell.
//
// Explorer creates a fresh console for the program and attaches nobody else to it, so
// the console's process list holds exactly one entry: us. A shell that launches us is
// attached to the same console, so the list holds at least two.
//
// This matters because a command-line tool that is double-clicked shows a black window,
// prints its usage into it, and closes before anyone can read a word. The binary now
// carries a graphical interface, so the natural gesture should reach it.
//
// Called through kernel32 rather than golang.org/x/sys/windows, which does not export
// this one. A lazy DLL lookup keeps the build free of C, which is the constraint that
// makes the six-target release matrix possible in the first place.
var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
)

func LaunchedByDoubleClick() bool {
	if err := procGetConsoleProcessList.Find(); err != nil {
		return false
	}
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])),
		uintptr(len(pids)),
	)
	// Zero means the call failed, which includes having no console at all. Neither that
	// nor a shared console is the case being caught, and guessing would open a browser
	// at the wrong moment, which is worse than printing usage.
	return n == 1
}

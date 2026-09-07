//go:build windows

package cli

import "syscall"

// notifyShell tells Explorer that file associations have changed.
//
// Without it the new entries appear only after a sign-out or a restart of Explorer, which
// looks exactly like the registration having failed. The registry writes above are real
// either way; this is what makes them visible now.
func notifyShell() {
	const shcneAssocChanged = 0x08000000
	const shcnfIdList = 0x0000

	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("SHChangeNotify")
	if proc.Find() != nil {
		return
	}
	_, _, _ = proc.Call(uintptr(shcneAssocChanged), uintptr(shcnfIdList), 0, 0)
}

package cli

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/bzhzion/noisecrypt/internal/webui"
)

func runGUI(env *Env, args []string) error {
	fs := newFlagSet(env, "gui", "[-no-browser]")
	noBrowser := fs.Bool("no-browser", false, "print the address instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := webui.New()
	if err != nil {
		return err
	}
	defer s.Close()

	fmt.Fprintf(env.Stdout, "NoiseCrypt interface on %s\n", s.Addr())
	fmt.Fprintln(env.Stdout, "  Loopback only, not reachable from the network.")
	fmt.Fprintln(env.Stdout, "  Stop it with Ctrl-C.")

	if *noBrowser {
		// The token is only printed when the user has asked to open the page
		// themselves. Printing it unconditionally would put a live credential in
		// every terminal scrollback and CI log for no reason.
		fmt.Fprintf(env.Stdout, "\n  %s\n", s.URL())
	} else {
		webui.OpenBrowser(s.URL())
		fmt.Fprintln(env.Stdout, "\n  Opening your browser. If nothing happens, run with -no-browser")
		fmt.Fprintln(env.Stdout, "  to print the address.")
	}

	if err := s.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

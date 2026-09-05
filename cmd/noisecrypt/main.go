// Command noisecrypt encrypts files into post-quantum sealed containers, on their
// way to becoming video.
package main

import (
	"os"

	"github.com/bzhzion/noisecrypt/internal/cli"
)

func main() {
	os.Exit(cli.Run(cli.DefaultEnv(), os.Args[1:]))
}

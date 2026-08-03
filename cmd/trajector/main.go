// Command trajector is the Trajector CLI: consent-gated capture of your
// own Claude Code sessions through a local proxy.
package main

import (
	"os"

	"github.com/PublicAI01/trajector-cli/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// Command trajector is the Trajector CLI: consent-gated capture of your
// own Claude Code sessions through a local proxy.
package main

import (
	"context"
	"os"

	"github.com/PublicAI01/trajector-cli/internal/cli"
)

func main() {
	// Nothing cancels a command from outside: an interrupt ends the
	// process, and the proxy modes watch for signals themselves.
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PublicAI01/trajector-cli/internal/lifecycle"
)

func (a *app) loginCmd(args []string) int {
	return a.run(args, "usage: trajector login", func(m *lifecycle.Machine) error {
		return m.Login(a.io())
	})
}

func (a *app) logoutCmd(args []string) int {
	return a.run(args, "usage: trajector logout", func(m *lifecycle.Machine) error {
		return m.Logout(a.io())
	})
}

func (a *app) enableCmd(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(a.stderr, "usage: trajector enable")
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		return a.fail(err)
	}
	m, err := a.machine()
	if err != nil {
		return a.fail(err)
	}
	if err := m.Enable(cwd, a.io()); err != nil {
		if errors.Is(err, lifecycle.ErrDeclined) {
			fmt.Fprintln(a.stdout, "Agreement declined; nothing was changed.")
			return 1
		}
		return a.fail(err)
	}
	return 0
}

func (a *app) disableCmd(args []string) int {
	purge := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--purge":
		purge = true
	default:
		fmt.Fprintln(a.stderr, "usage: trajector disable [--purge]")
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		return a.fail(err)
	}
	m, err := a.machine()
	if err != nil {
		return a.fail(err)
	}
	if err := m.Disable(cwd, purge, a.io()); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *app) statusCmd(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(a.stderr, "usage: trajector status")
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		return a.fail(err)
	}
	m, err := a.machine()
	if err != nil {
		return a.fail(err)
	}
	if err := m.Status(cwd, a.io()); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *app) uninstallCmd(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(a.stderr, "usage: trajector uninstall")
		return 2
	}
	m, err := a.machine()
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.stdout, "Delete local data (captured rawcalls, configuration, device token)? [y/N]: ")
	answer, _ := bufio.NewReader(a.stdin).ReadString('\n')
	deleteData := false
	if s := strings.ToLower(strings.TrimSpace(answer)); s == "y" || s == "yes" {
		deleteData = true
	}
	if err := m.Uninstall(deleteData, a.io()); err != nil {
		return a.fail(err)
	}
	env, err := resolveEnv()
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "Done. To finish, delete the binary itself: %s\n", env.execPath)
	return 0
}

// hookCmd hosts the commands injected into Claude Code hooks. They must
// never block a session: any failure is reported on stderr with a
// non-blocking exit code, and success is silent.
func (a *app) hookCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(a.stderr, "usage: trajector hook <ensure-proxy|discovery>")
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		return a.fail(err)
	}
	switch args[0] {
	case "ensure-proxy":
		m, err := a.machine()
		if err != nil {
			return a.fail(err)
		}
		if err := m.EnsureProxy(cwd, a.io()); err != nil {
			if errors.Is(err, lifecycle.ErrPortOccupied) {
				fmt.Fprintf(a.stderr, "trajector: WARNING: %v\n", err)
				fmt.Fprintln(a.stderr, "trajector: this project's API traffic is configured to route there. Investigate the")
				fmt.Fprintln(a.stderr, "trajector: process holding the port, or run `trajector disable` here to remove the routing.")
				return 1
			}
			return a.fail(err)
		}
		return 0
	case "discovery":
		// A lost hint is acceptable; a blocked session is not, so every
		// failure here is silent.
		m, err := a.machine()
		if err != nil {
			return 0
		}
		m.Discovery(cwd, a.io())
		return 0
	default:
		fmt.Fprintf(a.stderr, "trajector: unknown hook %q\n", args[0])
		return 2
	}
}

// run is the shape every argument-free command shares.
func (a *app) run(args []string, usage string, do func(*lifecycle.Machine) error) int {
	if len(args) != 0 {
		fmt.Fprintln(a.stderr, usage)
		return 2
	}
	m, err := a.machine()
	if err != nil {
		return a.fail(err)
	}
	if err := do(m); err != nil {
		return a.fail(err)
	}
	return 0
}

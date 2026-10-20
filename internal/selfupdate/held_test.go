package selfupdate_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/harness/procbin"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// The environment heldRelease reads: the program that holds its file,
// for how long, and the file the holder writes its pid to once it
// holds.
const (
	holderEnv  = "TRAJECTOR_TEST_HOLDER"
	holdForEnv = "TRAJECTOR_TEST_HOLD_FOR"
	heldEnv    = "TRAJECTOR_TEST_HELD"
)

// heldRelease is a 0.2.0 release whose first run, before it answers
// `version`, has another process open the release's own file and keep
// it open, as a scanner does with an executable that was just written
// and run. The holder is a separate program, so the file is not held
// as a running image is. The release accepts its install and finishes
// it.
func heldRelease(args []string) int {
	switch {
	case slices.Equal(args, []string{"version"}):
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		held := os.Getenv(heldEnv)
		holder := exec.Command(os.Getenv(holderEnv), "hold", self, os.Getenv(holdForEnv), held) //nolint:noctx // the holder outlives this run by design
		if err := holder.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for deadline := time.Now().Add(4 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(held); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "the holder did not open the release in time")
				return 1
			}
		}
		fmt.Println("trajector 0.2.0")
		return 0
	case len(args) == 4 && args[0] == "hold":
		hold, err := time.ParseDuration(args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		f, err := os.Open(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer f.Close()
		if err := fsatomic.WriteFile(args[3], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(hold)
		return 0
	default:
		return 0
	}
}

// upgradeHeldFor runs an upgrade of a machine on 0.1.0, whose binary
// is a stand-in file, to heldRelease, whose file is held open for hold
// from the moment its first run starts to answer. The holder does not
// outlive the test.
func upgradeHeldFor(t *testing.T, hold time.Duration) (m installed, release []byte, err error) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses to rename a file another process holds open")
	}
	m = install(t, "the 0.1.0 binary")
	holder, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if release, err = os.ReadFile(holder); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(t.TempDir(), "held")
	t.Setenv(holderEnv, holder)
	t.Setenv(holdForEnv, hold.String())
	t.Setenv(heldEnv, held)
	procbin.Self(t, "held release")
	t.Cleanup(func() {
		data, err := os.ReadFile(held)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			return
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
			_, _ = p.Wait()
		}
	})
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", release)

	_, err = runUpgrade(t, m.at("0.1.0"), releases.IndexURL())
	if _, statErr := os.Stat(held); statErr != nil {
		t.Fatalf("the release's file was never held open: %v", statErr)
	}
	return m, release, err
}

func TestUpgrade_ReleaseHeldOpenForAMomentAfterItsFirstRunIsStillInstalled(t *testing.T) {
	m, release, err := upgradeHeldFor(t, 2*time.Second)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if !bytes.Equal(m.bytesOf(), release) {
		t.Error("the release is not where the binary lives")
	}
	if got := m.siblings(); len(got) != 1 {
		t.Errorf("a committed upgrade left %v in the install directory", got)
	}
}

func TestUpgrade_ReleaseHeldOpenPastTheWaitLeavesThePreviousBinaryInPlace(t *testing.T) {
	m, _, err := upgradeHeldFor(t, time.Minute)
	if err == nil {
		t.Fatal("a release held open past the wait was installed")
	}
	if _, ok := errors.AsType[*selfupdate.UnusableError](err); ok {
		t.Errorf("Upgrade = %v, a release recorded as unusable for a file another program held", err)
	}
	if !strings.Contains(err.Error(), "installing over "+m.path) {
		t.Errorf("Upgrade = %v, want it to say the install over %s failed", err, m.path)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("the binary is %q, want the previous one in place", got)
	}
	if state := loadState(t, m.at("0.1.0")); len(state.Unusable) != 0 {
		t.Errorf("unusable versions are %v after an install a held file stopped", state.Unusable)
	}
}

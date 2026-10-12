package selfupdate_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PublicAI01/trajector-cli/internal/fsatomic"
	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/selfupdate"
)

// holdLock takes the upgrade lock of in the way another upgrade on the
// same machine would, until the test lets go of it or ends.
func holdLock(t *testing.T, in selfupdate.Installation) (release func()) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(in.Lock), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := fsatomic.Lock(in.Lock, time.Hour, 0)
	if err != nil {
		t.Fatalf("taking the upgrade lock: %v", err)
	}
	released := false
	release = func() {
		if !released {
			released = true
			unlock()
		}
	}
	t.Cleanup(release)
	return release
}

// leaveLock puts a lock file of the given age where in's upgrade lock
// lives: what an upgrade that died holding the lock leaves behind.
func leaveLock(t *testing.T, in selfupdate.Installation, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(in.Lock), 0o700); err != nil {
		t.Fatal(err)
	}
	// Taking and abandoning a lock without unlocking it is exactly what
	// a process that died mid-upgrade did.
	if _, err := fsatomic.Lock(in.Lock, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(in.Lock))
	if err != nil {
		t.Fatal(err)
	}
	then := time.Now().Add(-age)
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(filepath.Dir(in.Lock), e.Name()), then, then); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpgradeWhileAnotherUpgradeHoldsTheLockChangesNothing(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	holdLock(t, m.at("0.1.0"))

	_, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0)
	if !errors.Is(err, selfupdate.ErrBusy) {
		t.Fatalf("Upgrade = %v, want ErrBusy", err)
	}
	if downloads := releases.Downloads(); len(downloads) != 0 {
		t.Errorf("an upgrade that did not hold the lock downloaded %v", downloads)
	}
	if got := m.content(); got != "the 0.1.0 binary" {
		t.Errorf("installed binary is %q", got)
	}
	// Only the holder writes the state file, and this run never held it.
	if _, err := os.Stat(m.at("0.1.0").State); !os.IsNotExist(err) {
		t.Errorf("an upgrade that did not hold the lock wrote the state file (%v)", err)
	}
}

func TestUpgradeWaitsForAnUpgradeThatFinishesWithinTheWait(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	release := holdLock(t, m.at("0.1.0"))
	time.AfterFunc(100*time.Millisecond, release)

	out, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 2*time.Second)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if out.Kind != selfupdate.AlreadyNewest {
		t.Errorf("outcome is %+v, want the run to go ahead once the lock was free", out)
	}
}

func TestALockOlderThanFifteenMinutesIsTakenOver(t *testing.T) {
	// Fifteen minutes is longer than any live upgrade holds the lock: the
	// download is bounded at ten. A lock that old was left by an upgrade
	// that died, and waiting on it would stop upgrades for good.
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	leaveLock(t, m.at("0.1.0"), 16*time.Minute)

	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
}

func TestALockYoungerThanFifteenMinutesIsWaitedFor(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.1.0", []byte("the 0.1.0 binary"))
	leaveLock(t, m.at("0.1.0"), 14*time.Minute)

	if _, err := selfupdate.Upgrade(t.Context(), m.at("0.1.0"), releases.IndexURL(), 0); !errors.Is(err, selfupdate.ErrBusy) {
		t.Fatalf("Upgrade = %v, want ErrBusy: a slow upgrade may still be at work", err)
	}
}

func TestUpgradeWhoseBookkeepingCannotBeKeptChangesNothing(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	releases := fakereleases.New(t)
	releases.Publish(t, "0.2.0", []byte("the 0.2.0 binary"))
	// The state directory is a file: there is nowhere to take the lock.
	blocked := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := m.at("0.1.0")
	in.Lock = filepath.Join(blocked, "upgrade")

	_, err := selfupdate.Upgrade(t.Context(), in, releases.IndexURL(), 0)
	if err == nil || errors.Is(err, selfupdate.ErrBusy) {
		t.Fatalf("Upgrade = %v, want the lock's directory named as the failure", err)
	}
	if downloads := releases.Downloads(); len(downloads) != 0 {
		t.Errorf("an upgrade without the lock downloaded %v", downloads)
	}
}

func TestSweepResidueLeavesAnUpgradeInProgressAlone(t *testing.T) {
	m := install(t, "the 0.1.0 binary")
	// What a running upgrade has beside the binary is its work in
	// progress: the binary it is about to put in place, or the one it
	// may still have to put back.
	staged := m.path + ".incoming-4a1b"
	if err := os.WriteFile(staged, []byte("the 0.2.0 binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	release := holdLock(t, m.at("0.1.0"))

	selfupdate.SweepResidue(m.at("0.1.0"))
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("a sweep during an upgrade removed its staged binary: %v", err)
	}

	// Once that upgrade is over, what it left is residue like any other.
	release()
	selfupdate.SweepResidue(m.at("0.1.0"))
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("residue survived a sweep with no upgrade running (%v)", err)
	}
}

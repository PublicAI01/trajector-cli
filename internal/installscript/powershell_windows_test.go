package installscript

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/fakereleases"
	"github.com/PublicAI01/trajector-cli/internal/userdirs"
)

func launchPowerShell(t *testing.T, shell string, releases *fakereleases.Server, dir, version string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
	cmd.Env = os.Environ()
	userdirs.Isolate(func(key, value string) { cmd.Env = append(cmd.Env, key+"="+value) }, t.TempDir())
	cmd.Env = append(cmd.Env,
		"TRAJECTOR_API_BASE="+releases.APIBase(), "TRAJECTOR_DL_BASE="+releases.AssetBase(),
		"TRAJECTOR_INSTALL_DIR="+dir, "TRAJECTOR_VERSION="+version)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestPowerShellInstaller(t *testing.T) {
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); errors.Is(err, exec.ErrNotFound) {
				t.Skip("this PowerShell edition is not installed")
			} else if err != nil {
				t.Fatal(err)
			}
			binary := fakereleases.BuildRelease(t, "0.2.0")
			t.Run("highest version and a path with spaces", func(t *testing.T) {
				releases := fakereleases.New(t)
				releases.Publish(t, "0.2.0", binary)
				releases.Publish(t, "0.1.9", []byte("older"))
				releases.Publish(t, "0.2.0-rc.10", []byte("candidate"))
				releases.PublishDraft(t, "0.9.0", []byte("draft"))
				dir := filepath.Join(t.TempDir(), "install with spaces")
				out, err := launchPowerShell(t, shell, releases, dir, "")
				if err != nil {
					t.Fatalf("installer: %v\n%s", err, out)
				}
				got, err := os.ReadFile(filepath.Join(dir, "trajector.exe"))
				if err != nil || !bytes.Equal(got, binary) || reportedVersion(t, out) != "0.2.0" {
					t.Fatalf("wrong installed binary: %v\n%s", err, out)
				}
				if left := contents(t, dir); len(left) != 1 {
					t.Fatalf("temporary files left: %v", left)
				}
				if got := releases.Downloads(); !equal(got, []string{fakereleases.ChecksumsAsset, hostArchive()}) {
					t.Errorf("unexpected downloads: %v", got)
				}
			})
			t.Run("pinned version", func(t *testing.T) {
				releases := fakereleases.New(t)
				releases.Publish(t, "0.2.0", binary)
				releases.Publish(t, "0.3.0", []byte("newer"))
				out, err := launchPowerShell(t, shell, releases, t.TempDir(), "v0.2.0")
				if err != nil || reportedVersion(t, out) != "0.2.0" {
					t.Fatalf("installer: %v\n%s", err, out)
				}
			})
			for _, failure := range []string{"checksum", "missing checksum", "missing Windows build", "broken archive", "unusable binary", "wrong version", "existing installation"} {
				t.Run(failure, func(t *testing.T) {
					releases := fakereleases.New(t)
					dir := t.TempDir()
					version := ""
					switch failure {
					case "checksum":
						releases.Publish(t, "0.2.0", binary)
						releases.Corrupt(t, "0.2.0", "windows", runtime.GOARCH)
					case "missing checksum":
						releases.PublishWithout(t, "0.2.0", binary, fakereleases.ChecksumsAsset)
						version = "0.2.0"
					case "broken archive":
						releases.PublishBroken(t, "0.2.0")
					case "missing Windows build":
						releases.Publish(t, "0.1.0", []byte("obsolete Windows build"))
						releases.PublishWithout(t, "0.2.0", binary, hostArchive())
					case "unusable binary":
						releases.Publish(t, "0.2.0", []byte("not a Windows executable"))
					case "wrong version":
						releases.Publish(t, "0.3.0", binary)
					case "existing installation":
						releases.Publish(t, "0.2.0", binary)
						if err := os.WriteFile(filepath.Join(dir, "trajector.exe"), []byte("keep me"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					out, err := launchPowerShell(t, shell, releases, dir, version)
					if err == nil {
						t.Fatalf("invalid install succeeded: %s", out)
					}
					left := contents(t, dir)
					if failure == "existing installation" {
						got, err := os.ReadFile(filepath.Join(dir, "trajector.exe"))
						if err != nil || string(got) != "keep me" || !strings.Contains(out, "trajector upgrade") || len(left) != 1 || len(releases.Downloads()) != 0 {
							t.Fatalf("existing installation changed: %v\n%s", err, out)
						}
					} else if len(left) != 0 {
						t.Fatalf("failed install left %v\n%s", left, out)
					}
				})
			}
		})
	}
}

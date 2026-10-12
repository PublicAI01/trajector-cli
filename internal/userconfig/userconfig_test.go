package userconfig_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/userconfig"
)

func TestImagesAndDocumentsAreUploadedUnlessTheFileTurnsThemOff(t *testing.T) {
	cases := map[string]struct {
		content string
		omits   bool
	}{
		"no file":                {"", false},
		"no such option":         {`{"platform_url":"https://example.test"}`, false},
		"option turned on":       {`{"upload_images_and_documents":true}`, false},
		"option turned off":      {`{"upload_images_and_documents":false}`, true},
		"option explicitly null": {`{"upload_images_and_documents":null}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := userconfig.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OmitsImagesAndDocuments() != tc.omits {
				t.Fatalf("OmitsImagesAndDocuments = %v, want %v", !tc.omits, tc.omits)
			}
		})
	}
}

func TestUpgradesAreAutomaticUnlessTheFileTurnsThemOff(t *testing.T) {
	cases := map[string]struct {
		content   string
		automatic bool
	}{
		"no file":                {"", true},
		"no such option":         {`{"upload_images_and_documents":false}`, true},
		"option turned on":       {`{"auto_upgrade":true}`, true},
		"option turned off":      {`{"auto_upgrade":false}`, false},
		"option explicitly null": {`{"auto_upgrade":null}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := userconfig.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UpgradesAutomatically() != tc.automatic {
				t.Fatalf("UpgradesAutomatically = %v, want %v", !tc.automatic, tc.automatic)
			}
		})
	}
}

func TestAFileThatDoesNotReadTurnsImageUploadAndAutomaticUpgradeOffLeavesEveryOtherOptionAtItsDefaultAndSaysWhy(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"cut short":                       `{"upload_images_and_documents":true,"auto_upgrade":true,`,
		"one option of the wrong type":    `{"releases_url":"https://example.test","upload_images_and_documents":true,"auto_upgrade":true,"platform_url":5}`,
		"a directory where the file goes": "",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if content == "" {
				path = dir
			} else if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := userconfig.Read(path)
			readErr, ok := errors.AsType[*userconfig.ReadError](err)
			if !ok || readErr.Path != path {
				t.Fatalf("Read error = %v, want a ReadError naming %s", err, path)
			}
			if !strings.Contains(err.Error(), path) || errors.Unwrap(err) == nil {
				t.Fatalf("Read error = %q, want the path named and the cause kept", err)
			}
			if cfg.PlatformURL != "" || cfg.ReleasesURL != "" || !cfg.OmitsImagesAndDocuments() || cfg.UpgradesAutomatically() {
				t.Fatalf("Read = %+v, want image upload and automatic upgrade off and every other option at its default, none from the part that read", cfg)
			}
		})
	}
}

func TestTheImageSwitchKeyIsTheNameTheFileGivesIt(t *testing.T) {
	field, ok := reflect.TypeFor[userconfig.Config]().FieldByName("UploadImagesAndDocuments")
	if !ok {
		t.Fatal("Config has no image and document switch")
	}
	if tag := field.Tag.Get("json"); tag != userconfig.UploadImagesAndDocumentsKey {
		t.Fatalf("json tag = %q, want %q", tag, userconfig.UploadImagesAndDocumentsKey)
	}
}

func TestTheUpgradeSwitchKeyIsTheNameTheFileGivesIt(t *testing.T) {
	field, ok := reflect.TypeFor[userconfig.Config]().FieldByName("AutoUpgrade")
	if !ok {
		t.Fatal("Config has no automatic upgrade switch")
	}
	if tag := field.Tag.Get("json"); tag != "auto_upgrade" {
		t.Fatalf("json tag = %q, want %q", tag, "auto_upgrade")
	}
}

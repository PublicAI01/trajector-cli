package follow_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/follow"
)

func TestFile_React(t *testing.T) {
	tests := []struct {
		name string
		file follow.File
		stat follow.Stat
		want follow.Reaction
	}{
		{
			name: "file vanished",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: false},
			want: follow.Vanished,
		},
		{
			name: "vanished outranks a matching inode",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: false, Inode: 5, Size: 100},
			want: follow.Vanished,
		},
		{
			name: "inode changed",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: true, Inode: 6, Size: 200},
			want: follow.Rewrite,
		},
		{
			name: "file shrank",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: true, Inode: 5, Size: 50},
			want: follow.Rewrite,
		},
		{
			name: "file grew",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: true, Inode: 5, Size: 150},
			want: follow.Continue,
		},
		{
			name: "file unchanged",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: true, Inode: 5, Size: 100},
			want: follow.Continue,
		},
		{
			name: "first observation of a new registration",
			file: follow.File{},
			stat: follow.Stat{Exists: true, Inode: 5, Size: 150},
			want: follow.Continue,
		},
		{
			name: "observer without inodes never sees a rewrite by inode",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: true, Inode: 0, Size: 150},
			want: follow.Continue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.React(tt.stat); got != tt.want {
				t.Errorf("React() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFile_Behind(t *testing.T) {
	tests := []struct {
		name string
		file follow.File
		stat follow.Stat
		want int64
	}{
		{
			name: "bytes past the cursor",
			file: follow.File{Inode: 5, Size: 100, Offset: 80},
			stat: follow.Stat{Exists: true, Inode: 5, Size: 150},
			want: 70,
		},
		{
			name: "caught up",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: true, Inode: 5, Size: 100},
			want: 0,
		},
		{
			name: "rewrite is not behind",
			file: follow.File{Inode: 5, Size: 100, Offset: 100},
			stat: follow.Stat{Exists: true, Inode: 6, Size: 500},
			want: 0,
		},
		{
			name: "vanished is not behind",
			file: follow.File{Inode: 5, Size: 100, Offset: 10},
			stat: follow.Stat{Exists: false},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.Behind(tt.stat); got != tt.want {
				t.Errorf("Behind() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStatFile(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.jsonl")
	if err := os.WriteFile(present, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := follow.StatFile(present)
	if err != nil {
		t.Fatalf("StatFile(present) error: %v", err)
	}
	if !got.Exists || got.Size != 10 {
		t.Errorf("StatFile(present) = %+v, want Exists with Size 10", got)
	}
	if runtime.GOOS != "windows" && got.Inode == 0 {
		t.Errorf("StatFile(present) = %+v, want a non-zero inode", got)
	}

	missing, err := follow.StatFile(filepath.Join(dir, "missing.jsonl"))
	if err != nil {
		t.Fatalf("StatFile(missing) error: %v", err)
	}
	if missing != (follow.Stat{}) {
		t.Errorf("StatFile(missing) = %+v, want the zero Stat", missing)
	}

	if runtime.GOOS != "windows" {
		if _, err := follow.StatFile(filepath.Join(present, "under-a-file")); err == nil {
			t.Error("StatFile(path under a file) = nil error, want the failure surfaced")
		}
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateFilesRepairExistingPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vuja")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "history.db")
	if err := os.WriteFile(path, []byte("history"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := RestrictPrivateFiles(path); err != nil {
		t.Fatal(err)
	}

	assertPermission(t, dir, 0o700)
	assertPermission(t, path, 0o600)
}

func TestRestrictPrivateFilesRepairsSQLiteSidecarsAndAllowsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "history.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(base+suffix, []byte("history"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := RestrictPrivateFiles(base, base+"-wal", base+"-shm", base+"-missing"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		assertPermission(t, base+suffix, 0o600)
	}
}

func assertPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o; want %04o", path, got, want)
	}
}

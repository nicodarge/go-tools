package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilesAreOpenedInsideTheirDirectory(t *testing.T) {
	parent := t.TempDir()
	dir, sibling := filepath.Join(parent, "dir"), filepath.Join(parent, "sibling")
	for _, d := range []string{dir, sibling} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, text := range map[string]string{
		filepath.Join(dir, "inside.txt"):     "inside",
		filepath.Join(sibling, "secret.txt"): "secret",
	} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "sibling", "secret.txt"), filepath.Join(dir, "escape")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(dir, "stays")); err != nil {
		t.Fatal(err)
	}

	for _, ok := range []func() (*os.File, error){
		func() (*os.File, error) { return OpenIn(dir, "inside.txt") },
		func() (*os.File, error) { return OpenFile(filepath.Join(dir, "inside.txt")) },
		func() (*os.File, error) { return OpenIn(dir, "stays") },
	} {
		f, err := ok()
		if err != nil {
			t.Errorf("a file inside its directory opens: %v", err)
			continue
		}
		_ = f.Close()
	}
	for label, open := range map[string]func() (*os.File, error){
		"a symbolic link out of the directory":      func() (*os.File, error) { return OpenIn(dir, "escape") },
		"the same link named on the command line":   func() (*os.File, error) { return OpenFile(filepath.Join(dir, "escape")) },
		"a name with a parent element":              func() (*os.File, error) { return OpenIn(dir, "../sibling/secret.txt") },
		"a name with a parent element and a prefix": func() (*os.File, error) { return OpenIn(dir, "x/../../sibling/secret.txt") },
		"an absolute name":                          func() (*os.File, error) { return OpenIn(dir, filepath.Join(sibling, "secret.txt")) },
		"a directory that is not there":             func() (*os.File, error) { return OpenIn(filepath.Join(parent, "none"), "x") },
	} {
		if f, err := open(); err == nil {
			_ = f.Close()
			t.Errorf("%s must not open", label)
		}
	}
}

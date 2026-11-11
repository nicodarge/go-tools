package core

import (
	"os"
	"path/filepath"
)

// OpenFile opens the file at path for reading, through its directory as an os.Root, so that the
// name is resolved inside that directory only.
func OpenFile(path string) (*os.File, error) {
	return OpenIn(filepath.Dir(path), filepath.Base(path))
}

// OpenIn opens the file called name inside dir for reading. A name that would leave dir (a ".."
// element, a symbolic link pointing out) is an error.
func OpenIn(dir, name string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Open(name)
}

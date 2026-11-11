package golangciguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxConfigBytes = 1 << 20
	maxGoBytes     = 32 << 20
	configPrefix   = ".golangci."
)

// CheckPath judges the golangci-lint configuration that path names: a directory, which must hold
// exactly one `.golangci.*` entry, or that entry itself. golangci-lint reads the first of
// .golangci.yml, .golangci.yaml, .golangci.toml and .golangci.json it finds, so any entry of the
// directory with that prefix other than one YAML file is refused, as is a symbolic link or any
// other file that is not a regular file. The Go files under the directory must all be linted by
// every golangci-lint run (see checkBuildConstraints), and a nested module may hold none, as the
// run from the directory never enters it. Every go.mod and go.work under the directory is judged
// as well (see checkModuleFiles).
func CheckPath(path string, required []string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	dir, name := path, ""
	if !info.IsDir() {
		dir, name = filepath.Dir(path), filepath.Base(path)
		if !strings.HasPrefix(name, configPrefix) {
			return fmt.Errorf("%s is not named like a golangci-lint configuration (%s*)", name, configPrefix)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err = checkModule(root, dir, name, required); err != nil {
		return err
	}
	return checkModuleFiles(root)
}

// checkModule judges the configuration in root (name, when the caller named one) and the Go files
// under it. label names root in messages.
func checkModule(root *os.Root, label, name string, required []string) error {
	found, err := configEntries(root)
	if err != nil {
		return err
	}
	switch {
	case len(found) == 0:
		return fmt.Errorf("%s holds no %s*.yaml or %s*.yml", label, configPrefix, configPrefix)
	case len(found) > 1:
		return fmt.Errorf("%s holds more than one configuration entry: %s", label, strings.Join(found, ", "))
	case name != "" && found[0] != name:
		return fmt.Errorf("%s is not in %s", name, label)
	case found[0] != configPrefix+"yaml" && found[0] != configPrefix+"yml":
		return fmt.Errorf("%s is not YAML: only %syaml or %syml is accepted", found[0], configPrefix, configPrefix)
	}
	st, err := root.Lstat(found[0])
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", found[0])
	}
	data, err := readLimited(root, found[0], maxConfigBytes)
	if err != nil {
		return err
	}
	tags, err := checkConfig(data, required)
	if err != nil {
		return err
	}
	return checkBuildConstraints(root, tags)
}

func configEntries(root *os.Root) ([]string, error) {
	d, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	all, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range all {
		if strings.HasPrefix(e, configPrefix) {
			found = append(found, e)
		}
	}
	sort.Strings(found)
	return found, nil
}

func readLimited(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("the file is too large")
	}
	return data, nil
}

package golangciguard

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"golang.org/x/mod/modfile"
)

const maxModBytes = 1 << 20

// checkModuleFiles walks the whole tree under root, testdata and the other directories the go tool
// skips included, and judges every go.mod and go.work it meets. These files decide which Go files
// `./...` and the imports reach, so the Go-file walk is only valid once they are judged.
func checkModuleFiles(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && name == ".git" {
			return fs.SkipDir
		}
		switch name {
		case "go.work", "go.work.sum":
			return fmt.Errorf("%s: a workspace file puts modules into the lint that its directory does not hold, or takes them out", p)
		case "go.mod":
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s: not a regular file", p)
			}
			if err = checkGoMod(root, p); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
		}
		return nil
	})
}

// checkGoMod judges the go.mod at p, a path below root. It is parsed strictly, so a directive this
// guard's modfile does not know is refused instead of skipped.
func checkGoMod(root *os.Root, p string) error {
	data, err := readLimited(root, p, maxModBytes)
	if err != nil {
		return err
	}
	file, err := modfile.Parse(p, data, nil)
	if err != nil {
		return fmt.Errorf("cannot read the module file: %w", err)
	}
	dir := path.Dir(p)
	if skipped := skippedElement(dir); skipped != "" {
		return fmt.Errorf("a module in a directory the go tool skips (%s) is never linted but can still be reached by a replace directive or a workspace", skipped)
	}
	if len(file.Ignore) > 0 {
		return fmt.Errorf("an ignore directive (%s) hides a directory from ./..., so golangci-lint does not lint it", file.Ignore[0].Path)
	}
	for _, r := range file.Replace {
		if !modfile.IsDirectoryPath(r.New.Path) {
			continue
		}
		if terr := checkReplaceTarget(root, dir, r.New.Path); terr != nil {
			return fmt.Errorf("the replace directive for %s: %w", r.Old.Path, terr)
		}
	}
	return nil
}

// checkReplaceTarget accepts a local replacement only when it names a module the walk checks with a
// configuration of its own: a directory inside the tree, below no directory the go tool skips and
// behind no symbolic link, that holds a go.mod (a replaced module is built from its directory, so a
// Go file there reaches the build whatever golangci-lint lints). dir is the directory of the go.mod
// that holds the directive, below root.
func checkReplaceTarget(root *os.Root, dir, target string) error {
	if path.IsAbs(target) {
		return fmt.Errorf("%q is an absolute path, outside the directory checked", target)
	}
	resolved := path.Join(dir, target)
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%q leaves the directory checked", target)
	}
	if skipped := skippedElement(resolved); skipped != "" {
		return fmt.Errorf("%q is in a directory the go tool skips (%s), so its Go files are never linted", target, skipped)
	}
	walked := ""
	if resolved != "." {
		for _, element := range strings.Split(resolved, "/") {
			walked = path.Join(walked, element)
			st, err := root.Lstat(walked)
			if err != nil {
				return fmt.Errorf("cannot read %q: %w", target, err)
			}
			if !st.IsDir() {
				return fmt.Errorf("%q goes through %s, which is not a directory (a symbolic link is not followed)", target, walked)
			}
		}
	}
	st, err := root.Lstat(path.Join(resolved, "go.mod"))
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%q does not hold a go.mod that is a regular file", target)
	}
	return nil
}

// skippedElement returns the first element of the slash-separated dir that the go tool skips when it
// expands ./... (testdata, vendor, and names starting with _ or .), or "".
func skippedElement(dir string) string {
	if dir == "." {
		return ""
	}
	for _, element := range strings.Split(dir, "/") {
		if element == "testdata" || element == "vendor" || strings.HasPrefix(element, "_") || strings.HasPrefix(element, ".") {
			return element
		}
	}
	return ""
}

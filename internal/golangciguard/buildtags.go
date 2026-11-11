package golangciguard

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// alwaysTrue are the build tags that hold on every golangci-lint run on linux with the gc compiler;
// they are the only ones a constraint may use besides the configured run.build-tags.
var alwaysTrue = []string{"linux", "unix", "gc"}

// checkBuildConstraints walks the Go files under root and refuses anything golangci-lint may not
// lint, by allowlist: a lint run models one environment (the runner's architecture, cgo on or off,
// the toolchain's release tags), so a file is accepted only when it is linted in every one of them.
// prefix is the path of root below the top directory, with a trailing slash, and is put in front of
// every path reported.
func checkBuildConstraints(root *os.Root, prefix string, tags []string, required []string) error {
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p == "." {
				return nil
			}
			return checkDir(root, prefix, p, name, required)
		}
		if d.Type()&fs.ModeSymlink != 0 && !strings.HasSuffix(name, ".go") {
			return checkSymlink(root, prefix, p)
		}
		if !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s%s: not a regular file", prefix, p)
		}
		return checkGoFile(root, prefix, p, tags)
	})
}

// checkDir decides what the walk does with the directory p: a directory the go tool skips holds no
// Go file at all, a nested module is checked on its own, and anything else is walked.
func checkDir(root *os.Root, prefix, p, name string, required []string) error {
	switch {
	case name == ".git" || name == "testdata":
		return fs.SkipDir
	case strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "vendor":
		if found, err := firstGoFile(root, p); err != nil {
			return err
		} else if found != "" {
			return fmt.Errorf("%s%s: a Go file in a directory the go tool skips (%s) is never linted but can still be imported", prefix, found, name)
		}
		return fs.SkipDir
	}
	st, err := root.Lstat(filepath.Join(p, "go.mod"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !st.Mode().IsRegular():
		return fmt.Errorf("%s%s/go.mod: not a regular file", prefix, p)
	}
	if err := checkNestedModule(root, prefix, p, required); err != nil {
		return err
	}
	return fs.SkipDir
}

// checkNestedModule applies the whole check to the module in directory p, which golangci-lint run
// from the top directory does not enter: it needs a configuration of its own. A module that holds
// no Go file has nothing to lint and needs none.
func checkNestedModule(root *os.Root, prefix, p string, required []string) error {
	sub, err := root.OpenRoot(p)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Close() }()
	if found, ferr := firstGoFile(sub, "."); ferr != nil {
		return ferr
	} else if found == "" {
		return nil
	}
	return checkModule(sub, prefix+p, prefix+p+"/", "", required)
}

// checkSymlink refuses a symbolic link to a directory (the walk does not follow it, so the Go files
// behind it would go unchecked) and one that cannot be resolved inside the root.
func checkSymlink(root *os.Root, prefix, p string) error {
	st, err := root.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%s%s: cannot resolve the symbolic link: %w", prefix, p, err)
	case st.IsDir():
		return fmt.Errorf("%s%s: a symbolic link to a directory is not walked, so the Go files behind it are not checked", prefix, p)
	}
	return nil
}

// firstGoFile returns the path of a Go file under dir of root, or of a symbolic link to a directory
// (or one that cannot be resolved inside root), which the check refuses once it is reached; "" when
// there is none.
func firstGoFile(root *os.Root, dir string) (string, error) {
	sub, err := root.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = sub.Close() }()
	found := ""
	err = fs.WalkDir(sub.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if (!d.IsDir() && strings.HasSuffix(d.Name(), ".go")) || (d.Type()&fs.ModeSymlink != 0 && linksToDirectory(sub, p)) {
			found = path.Join(dir, p)
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

// linksToDirectory reports whether the symbolic link p of root is not a link the check can ignore:
// one to a directory, or one that cannot be resolved inside root. A dangling link is ignored.
func linksToDirectory(root *os.Root, p string) bool {
	st, err := root.Stat(p)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	return st.IsDir()
}

func checkGoFile(root *os.Root, prefix, p string, tags []string) error {
	where := prefix + p
	ok, err := fileNameIsLinux(path.Base(p))
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if !ok {
		return fmt.Errorf("%s: its file name carries a GOOS or GOARCH suffix other than _linux, so a lint run on another platform or architecture does not lint it", where)
	}
	src, err := readLimited(root, p, maxGoBytes)
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), where, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return fmt.Errorf("%s: cannot read its build constraint: %w", where, err)
	}
	if err = checkImports(file); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if err = checkConstraintLines(file, tags); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	return nil
}

// fileNameIsLinux reports whether the go tool applies no GOOS or GOARCH file name constraint other
// than _linux to name: it asks go/build, whose list of known systems and architectures is the
// toolchain's, with the operating system linux and an architecture that does not exist, which only
// a name without an architecture suffix satisfies.
func fileNameIsLinux(name string) (bool, error) {
	ctxt := build.Context{
		GOOS:     "linux",
		GOARCH:   "none",
		Compiler: "gc",
		OpenFile: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("package p\n")), nil },
	}
	return ctxt.MatchFile(".", name)
}

// checkImports refuses the import of "C" (cgo is on or off depending on where the lint runs) and
// of a testdata directory, which the go tool skips and golangci-lint never lints.
func checkImports(file *ast.File) error {
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return fmt.Errorf("cannot read the import %s: %w", imp.Path.Value, err)
		}
		if importPath == "C" {
			return errors.New(`it imports "C": cgo is on or off depending on where golangci-lint runs`)
		}
		if slices.Contains(strings.Split(importPath, "/"), "testdata") {
			return fmt.Errorf("it imports %q, a testdata directory the go tool skips, so the Go files there are never linted", importPath)
		}
	}
	return nil
}

// checkConstraintLines judges every //go:build and // +build line before the package clause. The
// lines are located through the parser's comments, not the line scanner of go/build, so a constraint
// is refused when either would read it.
func checkConstraintLines(file *ast.File, tags []string) error {
	goBuild := 0
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, c := range group.List {
			var expr constraint.Expr
			var err error
			switch {
			case constraint.IsGoBuild(c.Text):
				goBuild++
				expr, err = constraint.Parse(c.Text)
			case constraint.IsPlusBuild(c.Text):
				expr, err = constraint.Parse(c.Text)
			default:
				continue
			}
			if err != nil {
				return fmt.Errorf("cannot read its build constraint: %w", err)
			}
			if err = checkExpr(expr, false, tags); err != nil {
				return err
			}
		}
	}
	if goBuild > 1 {
		return errors.New("cannot read its build constraint: more than one //go:build line")
	}
	return nil
}

// checkExpr accepts a constraint only when every tag it names is one that holds on every lint run
// (alwaysTrue or a configured tag) and none of them is negated, so that the constraint is true
// whatever the environment. negated is the parity of the negations above expr.
func checkExpr(expr constraint.Expr, negated bool, tags []string) error {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return checkTerm(e.Tag, negated, tags)
	case *constraint.NotExpr:
		return checkExpr(e.X, !negated, tags)
	case *constraint.AndExpr:
		return checkBoth(e.X, e.Y, negated, tags)
	case *constraint.OrExpr:
		return checkBoth(e.X, e.Y, negated, tags)
	}
	return fmt.Errorf("cannot read its build constraint: unknown expression %T", expr)
}

func checkBoth(x, y constraint.Expr, negated bool, tags []string) error {
	if err := checkExpr(x, negated, tags); err != nil {
		return err
	}
	return checkExpr(y, negated, tags)
}

func checkTerm(tag string, negated bool, tags []string) error {
	configured := slices.Contains(tags, tag)
	switch {
	case !configured && !slices.Contains(alwaysTrue, tag):
		return fmt.Errorf("its build constraint names %q, which is neither linux, unix, gc nor a run.build-tags entry, so a lint run does not always build the file", tag)
	case negated && configured:
		return fmt.Errorf("its build constraint negates the run.build-tags entry %q, so setting the tag takes the file out of the lint", tag)
	case negated:
		return fmt.Errorf("its build constraint negates %q, which holds on every lint run, so the file is never linted", tag)
	}
	return nil
}

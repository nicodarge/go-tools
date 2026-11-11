package lockfilecheck

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainExitCodes(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := write("base", lockText("1.0.0", intA))
	head := write("head", lockText("1.0.1", intB))
	pages := honestPages()
	manifests := filepath.Join(dir, "manifests")
	if err := os.Mkdir(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	write("manifests/foo@1.0.1.json", []byte(pages["/foo/1.0.1"]))
	write("manifests/foo.packument.json", []byte(pages["/foo"]))

	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := Main(args, &out)
		return code, strings.TrimSpace(out.String())
	}
	if code, out := run("--manifests", manifests, "--before", "2030-01-01T00:00:00Z", "package-lock.json", base, head); code != 0 || out != "PASS" {
		t.Errorf("a bump the registry publishes passes: %d %q", code, out)
	}
	if code, out := run("--manifests", manifests, "package-lock.json", base, head); code != 1 || out != "FAIL: no time to hold the new versions against" {
		t.Errorf("a new version with no --before is refused: %d %q", code, out)
	}
	if code, out := run("--manifests", manifests, "--before", "2030-01-01T00:00:00Z", "yarn.lock", base, head); code != 1 || out != "FAIL: yarn.lock is not a lockfile this knows" {
		t.Errorf("another lockfile is refused: %d %q", code, out)
	}
	if code, out := run("--manifests", manifests, "--before", "2030-01-01T00:00:00Z", "package-lock.json", base, filepath.Join(dir, "missing")); code != 1 || out != "FAIL: internal error (FileNotFoundError)" {
		t.Errorf("a file that cannot be read is refused: %d %q", code, out)
	}
	if code, out := run("package-lock.json", base); code != 1 || !strings.HasPrefix(out, "FAIL: usage: ") {
		t.Errorf("too few arguments is a usage refusal: %d %q", code, out)
	}
}

package checksumcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainExitCodes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	mapping := write("checksums.py", testMapping)
	base := write("base", pins(envBaseTemplate))
	head := write("head", pins(strings.Replace(envBaseTemplate, "ALLOY_VERSION=1.21.2", "ALLOY_VERSION=1.21.3", 1)))

	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := Main(args, &out)
		return code, strings.TrimSpace(out.String())
	}
	if code, out := run(mapping, ".github/tool.sh", base, head); code != 1 || !strings.HasSuffix(out, "is neither a Dockerfile nor a tool-pin env file") {
		t.Errorf("a path that is no pin file is refused: %d %q", code, out)
	}
	if code, out := run(mapping, ".github/tool.env", base, head); code != 1 || out != "FAIL: .github/tool.env changes a version with no checksum beside it" {
		t.Errorf("a version that moves alone in an env file is refused: %d %q", code, out)
	}
	if code, out := run(filepath.Join(dir, "missing"), ".github/tool.env", base, head); code != 1 || out != "FAIL: internal error (FileNotFoundError)" {
		t.Errorf("a mapping that cannot be read is refused: %d %q", code, out)
	}
	if code, out := run(mapping, ".github/tool.env"); code != 1 || !strings.HasPrefix(out, "FAIL: usage: ") {
		t.Errorf("too few arguments is a usage refusal: %d %q", code, out)
	}
}

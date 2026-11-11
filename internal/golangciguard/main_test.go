package golangciguard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(args ...string) (int, string) {
	var out bytes.Buffer
	code := Main(args, &out)
	return code, out.String()
}

func TestUsage(t *testing.T) {
	for label, args := range map[string][]string{
		"no arguments":         nil,
		"an unknown command":   {"lint"},
		"scan-go no files":     {"scan-go"},
		"an unknown flag":      {"config", "--skip", "."},
		"a flag with no value": {"config", "--require"},
	} {
		code, out := run(args...)
		if code != 1 || !strings.HasPrefix(out, "FAIL: usage:") {
			t.Errorf("%s: exit %d, output %q", label, code, out)
		}
	}
}

func TestConfigCommand(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".golangci.yaml", validConfig)
	bad := t.TempDir()
	write(t, bad, ".golangci.yaml", edit(t, "max-same-issues: 0", "max-same-issues: 1"))

	if code, out := run("config", dir); code != 0 || out != "PASS\n" {
		t.Errorf("a good directory: exit %d, output %q", code, out)
	}
	if code, out := run("config", "--require", "gosec,revive", dir); code != 0 || out != "PASS\n" {
		t.Errorf("a smaller required set: exit %d, output %q", code, out)
	}
	if code, out := run("config", "--require", "gosec,wsl_v5", dir); code != 1 || !strings.Contains(out, "linters.enable lacks wsl_v5") || strings.Contains(out, "PASS") {
		t.Errorf("a required linter that is not enabled: exit %d, output %q", code, out)
	}
	if code, out := run("config", "--require", "", dir); code != 1 || !strings.Contains(out, "is not a linter name") {
		t.Errorf("an empty required set: exit %d, output %q", code, out)
	}
	code, out := run("config", dir, bad)
	if code != 1 || !strings.Contains(out, "FAIL: "+bad+": issues.max-same-issues must be exactly 0\n") || strings.Contains(out, "PASS") {
		t.Errorf("one bad directory among two: exit %d, output %q", code, out)
	}
	if code, out := run("config", filepath.Join(dir, "missing")); code != 1 || !strings.HasPrefix(out, "FAIL: ") {
		t.Errorf("a path that is not there: exit %d, output %q", code, out)
	}
}

func TestConfigCommandDefaultsToTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".golangci.yaml", validConfig)
	t.Chdir(dir)
	if code, out := run("config"); code != 0 || out != "PASS\n" {
		t.Errorf("a good working directory: exit %d, output %q", code, out)
	}
	if err := os.Remove(filepath.Join(dir, ".golangci.yaml")); err != nil {
		t.Fatal(err)
	}
	if code, out := run("config"); code != 1 || !strings.Contains(out, "holds no") {
		t.Errorf("a working directory with no configuration: exit %d, output %q", code, out)
	}
}

func TestScanGoCommand(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "good.go", "package p\n")
	bad := write(t, dir, "bad.go", "package p\n\n//nolint\nvar x = 1\n")

	if code, out := run("scan-go", good); code != 0 || out != "PASS\n" {
		t.Errorf("a good file: exit %d, output %q", code, out)
	}
	code, out := run("scan-go", good, bad)
	if code != 1 || !strings.Contains(out, "FAIL: "+bad+": ") || strings.Contains(out, "PASS") {
		t.Errorf("a bad file among two: exit %d, output %q", code, out)
	}
	if code, out := run("scan-go", filepath.Join(dir, "missing.go")); code != 1 || !strings.HasPrefix(out, "FAIL: ") {
		t.Errorf("a file that is not there: exit %d, output %q", code, out)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestAPassNobodyCouldReadIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "good.go", "package p\n")
	if code := Main([]string{"scan-go", good}, brokenWriter{}); code != 1 {
		t.Errorf("exit %d with an unwritable output", code)
	}
}

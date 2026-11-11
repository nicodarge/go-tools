package golangciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheRepositoryConfigurationIsAccepted(t *testing.T) {
	if err := CheckPath(filepath.Join("..", ".."), DefaultRequired); err != nil {
		t.Fatalf("the configuration of this repository is refused: %v", err)
	}
	if err := CheckPath(filepath.Join("..", "..", ".golangci.yaml"), DefaultRequired); err != nil {
		t.Fatalf("the configuration of this repository is refused when named as a file: %v", err)
	}
}

func TestConfigurationLocation(t *testing.T) {
	ok := t.TempDir()
	write(t, ok, ".golangci.yaml", validConfig)
	write(t, ok, "other.toml", "x = 1\n")
	write(t, ok, ".golangcilint", "not a configuration entry\n")
	yml := t.TempDir()
	write(t, yml, ".golangci.yml", validConfig)
	for label, path := range map[string]string{
		"a directory":      ok,
		"a file":           filepath.Join(ok, ".golangci.yaml"),
		"a .yml directory": yml,
		"a .yml file":      filepath.Join(yml, ".golangci.yml"),
	} {
		if err := CheckPath(path, DefaultRequired); err != nil {
			t.Errorf("%s is refused: %v", label, err)
		}
	}

	type layout map[string]string
	for label, tc := range map[string]struct {
		files layout
		arg   string // relative to the directory; empty names the directory itself
		want  string
	}{
		"a TOML configuration only":         {layout{".golangci.toml": "x = 1\n"}, "", "is not YAML"},
		"a JSON configuration only":         {layout{".golangci.json": "{}\n"}, "", "is not YAML"},
		"a TOML file named as the argument": {layout{".golangci.toml": "x = 1\n"}, ".golangci.toml", "is not YAML"},
		"YAML and TOML":                     {layout{".golangci.yaml": validConfig, ".golangci.toml": "x = 1\n"}, "", "more than one configuration entry"},
		"YAML and JSON":                     {layout{".golangci.yaml": validConfig, ".golangci.json": "{}\n"}, "", "more than one configuration entry"},
		"YAML file next to a TOML one":      {layout{".golangci.yaml": validConfig, ".golangci.toml": "x = 1\n"}, ".golangci.yaml", "more than one configuration entry"},
		"yaml and yml":                      {layout{".golangci.yaml": validConfig, ".golangci.yml": validConfig}, "", "more than one configuration entry"},
		"a different suffix":                {layout{".golangci.yaml.bak": validConfig}, "", "is not YAML"},
		"a dist suffix":                     {layout{".golangci.dist": validConfig}, "", "is not YAML"},
		"no configuration":                  {layout{"README.md": "x\n"}, "", "holds no"},
		"a file with another name":          {layout{"lint.yaml": validConfig}, "lint.yaml", "is not named like"},
		"a file that is not there":          {layout{".golangci.yaml": validConfig}, ".golangci.yml", "no such file"},
		"a refused configuration":           {layout{".golangci.yaml": edit(t, "max-same-issues: 0", "max-same-issues: 9")}, "", "issues.max-same-issues"},
		"an empty configuration":            {layout{".golangci.yaml": ""}, "", "the file is empty"},
		"a configuration over the cap":      {layout{".golangci.yaml": validConfig + "#" + strings.Repeat("x", maxConfigBytes)}, "", "too large"},
	} {
		dir := t.TempDir()
		for name, text := range tc.files {
			write(t, dir, name, text)
		}
		err := CheckPath(filepath.Join(dir, tc.arg), DefaultRequired)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", label, err, tc.want)
		}
	}
}

func TestConfigurationThatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".golangci.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckPath(dir, DefaultRequired); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a directory named like the configuration: %v", err)
	}

	for label, target := range map[string]string{"inside": "real.yaml", "outside": filepath.Join("..", "real.yaml")} {
		parent := t.TempDir()
		dir := filepath.Join(parent, "repo")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, parent, "real.yaml", validConfig)
		write(t, dir, "real.yaml", validConfig)
		if err := os.Symlink(target, filepath.Join(dir, ".golangci.yaml")); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		for _, path := range []string{dir, filepath.Join(dir, ".golangci.yaml")} {
			if err := CheckPath(path, DefaultRequired); err == nil {
				t.Errorf("a symbolic link to a file %s the directory is accepted (%s)", label, path)
			}
		}
	}

	if err := CheckPath(filepath.Join(t.TempDir(), "missing"), DefaultRequired); err == nil {
		t.Error("a path that does not exist is accepted")
	}
}

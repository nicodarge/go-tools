package lockfilecheck

import (
	"testing"

	"github.com/nicodarge/go-tools/internal/core"
)

func TestSatisfies(t *testing.T) {
	cases := []struct {
		version, spec string
		want          bool
	}{
		{"1.2.3", "^1.0.0", true},
		{"2.0.0", "^1.0.0", false},
		{"0.2.5", "^0.2.3", true},
		{"0.3.0", "^0.2.3", false},
		{"1.5.0", "~1.5.0", true},
		{"1.6.0", "~1.5.0", false},
		{"1.2.3-beta.1", "^1.0.0", false},
		{"1.2.3-beta.1", ">=1.2.3-alpha", true},
		{"1.2.4-beta.1", ">=1.2.3-alpha", false},
		{"3.0.0", ">=1.0.0 <2.0.0 || >=3.0.0", true},
		{"99999999999999999999.0.0", ">=99999999999999999998.0.0", true},
		{"1.0.0", "*", true},
		{"1.2.3", "1.x", true},
		{"1.2.3", "v1.2.3", true},
	}
	for _, c := range cases {
		if got := satisfiesStr(c.version, c.spec); got != c.want {
			t.Errorf("%s in %s: got %v", c.version, c.spec, got)
		}
	}
}

func TestSatisfiesRefusesWhatItCannotRead(t *testing.T) {
	for _, spec := range []string{"1.0.0 - 2.0.0", "latest", "npm:foo@1", "git+https://x/y.git", "file:../x", "^1.0.0.tgz", ""} {
		if core.Guard(func() { satisfiesStr("1.0.0", spec) }) == "" {
			t.Errorf("%q must refuse", spec)
		}
	}
	for _, version := range []string{"1.0", "v1.0.0", "01.0.0", "1.0.0-", "latest"} {
		if core.Guard(func() { satisfiesStr(version, "*") }) == "" {
			t.Errorf("version %q must refuse", version)
		}
	}
}

func TestVersionOrder(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := 0; i+1 < len(order); i++ {
		if cmpVersion(parseVersion(order[i]), parseVersion(order[i+1])) >= 0 {
			t.Errorf("%s must sort before %s", order[i], order[i+1])
		}
	}
}

package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestString(t *testing.T) {
	t.Cleanup(func() { Version = "" })

	Version = "v1.2.3"
	if got := String(); got != "v1.2.3" {
		t.Errorf("String() = %q, want the injected version", got)
	}
}

func TestFromBuildInfo(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"no build info", nil, false, "dev"},
		{"build info flagged unavailable", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, false, "dev"},
		{"development build", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, "dev"},
		{"empty module version", &debug.BuildInfo{}, true, "dev"},
		{"module version", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true, "v1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fromBuildInfo(tt.info, tt.ok); got != tt.want {
				t.Errorf("fromBuildInfo() = %q, want %q", got, tt.want)
			}
		})
	}
}

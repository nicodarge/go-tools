package buildinfo

import "testing"

func TestString(t *testing.T) {
	t.Cleanup(func() { Version = "" })

	Version = "v1.2.3"
	if got := String(); got != "v1.2.3" {
		t.Errorf("String() = %q, want the injected version", got)
	}

	Version = ""
	if got := String(); got == "" {
		t.Error("String() is empty without an injected version")
	}
}

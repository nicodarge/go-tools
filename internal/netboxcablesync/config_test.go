package netboxcablesync

import (
	"strings"
	"testing"
)

func TestConfigurationErrorsExitTwoWithoutSecrets(t *testing.T) {
	good := func() map[string]string {
		return map[string]string{
			"UNIFI_URL": "http://127.0.0.1:1", "UNIFI_API_KEY": unifiKey,
			"NETBOX_URL": "http://127.0.0.1:1", "NETBOX_TOKEN": netboxSecret,
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]string)
		args   []string
		want   string
	}{
		{"no UniFi URL", func(m map[string]string) { delete(m, "UNIFI_URL") }, nil, "no UniFi URL"},
		{"no NetBox URL", func(m map[string]string) { delete(m, "NETBOX_URL") }, nil, "no NetBox URL"},
		{"no UniFi key", func(m map[string]string) { delete(m, "UNIFI_API_KEY") }, nil, "no UniFi API key"},
		{"no NetBox token", func(m map[string]string) { delete(m, "NETBOX_TOKEN") }, nil, "no NetBox token"},
		{"plain http to a remote host", func(m map[string]string) { m["UNIFI_URL"] = "http://unifi.example" }, nil, "must be https"},
		{"credentials in the URL", func(m map[string]string) { m["NETBOX_URL"] = "https://user:pw@netbox.example" }, nil, "without credentials"},
		{"not a URL", func(m map[string]string) { m["NETBOX_URL"] = "netbox.example" }, nil, "not an http(s) URL"},
		{"bad site", func(map[string]string) {}, []string{"--unifi-site", "a/b"}, "plain site name"},
		{"bad path", func(map[string]string) {}, []string{"--unifi-api-path", "/a/../b?x=1"}, "API path"},
		{"dot site", func(map[string]string) {}, []string{"--unifi-site", ".."}, "plain site name"},
		{"dot-dot path segment", func(map[string]string) {}, []string{"--unifi-api-path", "/a/../b"}, "API path"},
		{"dot path segment", func(map[string]string) {}, []string{"--unifi-api-path", "/./b"}, "API path"},
		{"bad tag", func(map[string]string) {}, []string{"--tag", "a b"}, "NetBox slug"},
		{"unknown flag", func(map[string]string) {}, []string{"--nope"}, "bad arguments"},
		{"positional argument", func(map[string]string) {}, []string{"extra"}, "unexpected argument"},
		{"missing CA file", func(map[string]string) {}, []string{"--unifi-ca-file", "/nonexistent/ca.pem"}, "CA file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := good()
			tt.mutate(env)
			r := runWith(t, env, tt.args...)
			if r.code != 2 || !strings.Contains(r.errOut, tt.want) {
				t.Errorf("exit %d, stderr %q, want 2 and %q", r.code, r.errOut, tt.want)
			}
			r.noSecrets(t)
		})
	}
}

func TestSecretsAreNeverFlags(t *testing.T) {
	env := map[string]string{
		"UNIFI_URL": "http://127.0.0.1:1", "UNIFI_API_KEY": unifiKey,
		"NETBOX_URL": "http://127.0.0.1:1", "NETBOX_TOKEN": netboxSecret,
	}
	for _, flag := range []string{"--unifi-api-key", "--netbox-token"} {
		r := runWith(t, env, flag, "secret-value-0123")
		if r.code != 2 || !strings.Contains(r.errOut, "bad arguments") || !strings.Contains(r.errOut, "flag provided but not defined") {
			t.Errorf("%s: exit %d, stderr %q", flag, r.code, r.errOut)
		}
		if strings.Contains(r.out+r.errOut, "secret-value-0123") {
			t.Errorf("%s: the value is echoed: %q", flag, r.errOut)
		}
		r.noSecrets(t)
	}
}

func TestHelpPrintsTheUsageAndExitsZero(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		r := runWith(t, map[string]string{}, flag)
		if r.code != 0 || !strings.Contains(r.out, "usage: netbox-cable-sync") || r.errOut != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", flag, r.code, r.out, r.errOut)
		}
	}
}

func TestUnifiAuthenticationFailureExitsTwoWithoutTheKey(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := runWith(t, map[string]string{
		"UNIFI_URL": u.srv.URL, "UNIFI_API_KEY": "wrong-key-value",
		"NETBOX_URL": n.srv.URL, "NETBOX_TOKEN": netboxSecret,
	})
	if r.code != 2 || !strings.Contains(r.errOut, "HTTP 401") || strings.Contains(r.errOut, "wrong-key-value") {
		t.Errorf("exit %d, stderr %q", r.code, r.errOut)
	}
	r.noSecrets(t)
}

func TestNetboxAuthenticationFailureExitsTwo(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := runWith(t, map[string]string{
		"UNIFI_URL": u.srv.URL, "UNIFI_API_KEY": unifiKey,
		"NETBOX_URL": n.srv.URL, "NETBOX_TOKEN": "wrong-token-value",
	})
	if r.code != 2 || !strings.Contains(r.errOut, "HTTP 403") || strings.Contains(r.errOut, "wrong-token-value") {
		t.Errorf("exit %d, stderr %q", r.code, r.errOut)
	}
}

func TestEmptyUnifiInventoryIsRefused(t *testing.T) {
	u := newFakeUnifi(t, nil, nil)
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u, "--apply", "--prune")
	if r.code != 2 || !strings.Contains(r.errOut, "no device") || len(n.writes) != 0 {
		t.Errorf("exit %d, stderr %q, writes %v", r.code, r.errOut, n.writes)
	}
}

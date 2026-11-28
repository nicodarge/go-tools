package netboxcablesync

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestRedirectIsNeverFollowed(t *testing.T) {
	var hits atomic.Int32
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-API-KEY") != "" || r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()

	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"UniFi", map[string]string{"UNIFI_URL": redirector.URL, "NETBOX_URL": n.srv.URL}, "HTTP 302"},
		{"NetBox", map[string]string{"UNIFI_URL": u.srv.URL, "NETBOX_URL": redirector.URL}, "HTTP 302"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.env["UNIFI_API_KEY"] = unifiKey
			tt.env["NETBOX_TOKEN"] = netboxSecret
			r := runWith(t, tt.env)
			if r.code != 2 || !strings.Contains(r.errOut, tt.want) {
				t.Errorf("exit %d, stderr %q", r.code, r.errOut)
			}
			r.noSecrets(t)
		})
	}
	if hits.Load() != 0 || leaked.Load() {
		t.Errorf("the redirect target was reached %d times, credential leaked: %v", hits.Load(), leaked.Load())
	}
}

func TestCAFileTrustsAPrivateCertificate(t *testing.T) {
	u := newFakeUnifiTLS(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: u.srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"UNIFI_URL": u.srv.URL, "UNIFI_API_KEY": unifiKey,
		"NETBOX_URL": n.srv.URL, "NETBOX_TOKEN": netboxSecret,
	}
	t.Run("without the CA file", func(t *testing.T) {
		r := runClient(t, env, nil)
		if r.code != 2 || !strings.Contains(r.errOut, "request failed") {
			t.Errorf("exit %d, stderr %q", r.code, r.errOut)
		}
		if len(u.paths) != 0 {
			t.Errorf("an untrusted server was queried: %v", u.paths)
		}
	})
	t.Run("with the CA file flag", func(t *testing.T) {
		r := runClient(t, env, nil, "--unifi-ca-file", caFile)
		if r.code != 0 || !strings.Contains(r.out, "summary:") {
			t.Errorf("exit %d, stderr %q", r.code, r.errOut)
		}
	})
	t.Run("with the CA file variable", func(t *testing.T) {
		withVar := map[string]string{"UNIFI_CA_FILE": caFile}
		for k, v := range env {
			withVar[k] = v
		}
		if r := runClient(t, withVar, nil); r.code != 0 {
			t.Errorf("exit %d, stderr %q", r.code, r.errOut)
		}
	})
	t.Run("a file with no certificate", func(t *testing.T) {
		empty := filepath.Join(t.TempDir(), "empty.pem")
		if err := os.WriteFile(empty, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		if r := runClient(t, env, nil, "--unifi-ca-file", empty); r.code != 2 || !strings.Contains(r.errOut, "no certificate") {
			t.Errorf("exit %d, stderr %q", r.code, r.errOut)
		}
	})
}

func TestPlainHTTPIsLoopbackOnly(t *testing.T) {
	tests := map[string]bool{
		"http://localhost:8443":    true,
		"http://LOCALHOST":         true,
		"http://127.0.0.1:80":      true,
		"http://[::1]:8443":        true,
		"https://unifi.example":    true,
		"http://unifi.example":     false,
		"http://10.0.0.5":          false,
		"http://[2001:db8::1]:80":  false,
		"http://localhost.example": false,
	}
	for raw, ok := range tests {
		_, err := newAPIClient("UniFi", raw, "", nil, nil)
		if (err == nil) != ok {
			t.Errorf("%s: accepted %v, want %v (%v)", raw, err == nil, ok, err)
		}
	}
}

func TestLocalhostIsReachedOverHTTP(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := runWith(t, map[string]string{
		"UNIFI_URL": strings.Replace(u.srv.URL, "127.0.0.1", "localhost", 1), "UNIFI_API_KEY": unifiKey,
		"NETBOX_URL": n.srv.URL, "NETBOX_TOKEN": netboxSecret,
	})
	if r.code != 0 {
		t.Errorf("exit %d: %s", r.code, r.errOut)
	}
}

func TestErrorMessagesAreBoundedAndPrintable(t *testing.T) {
	long := `{"detail":"` + strings.Repeat("é", 300) + `"}`
	got := errorMessage([]byte(long))
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != maxMessage+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("truncated to %d runes, valid %v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if got := errorMessage([]byte("a\x07b\x1b[31m‮c\n\td")); got != "ab[31mc d" {
		t.Errorf("control characters kept: %q", got)
	}
}

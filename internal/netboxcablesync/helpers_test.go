package netboxcablesync

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	unifiKey     = "unifi-secret-key-0123"
	netboxSecret = "netbox-secret-token-4567"

	macSW1 = "00:00:00:00:00:01"
	macGW  = "00:00:00:00:00:02"
	macPi  = "a0:bb:cc:00:00:01"
	macNAS = "a0:bb:cc:00:00:02"
)

type fakeUnifi struct {
	srv     *httptest.Server
	mu      sync.Mutex
	devices []any
	clients []any
	prefix  string
	paths   []string
}

func newFakeUnifi(t *testing.T, devices, clients []any) *fakeUnifi {
	t.Helper()
	f := &fakeUnifi{devices: devices, clients: clients, prefix: "/proxy/network/api/s/default"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func newFakeUnifiTLS(t *testing.T, devices, clients []any) *fakeUnifi {
	t.Helper()
	f := &fakeUnifi{devices: devices, clients: clients, prefix: "/proxy/network/api/s/default"}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUnifi) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	if r.Header.Get("X-API-KEY") != unifiKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"meta":{"rc":"error","msg":"api.err.LoginRequired"}}`))
		return
	}
	var data []any
	switch r.URL.Path {
	case f.prefix + "/stat/device":
		data = f.devices
	case f.prefix + "/stat/sta":
		data = f.clients
	default:
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": map[string]string{"rc": "ok"}, "data": data})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type fakeNetbox struct {
	srv        *httptest.Server
	mu         sync.Mutex
	devices    map[int]string
	ifaces     []map[string]any
	cables     []map[string]any
	tags       []map[string]any
	nextID     int
	maxPage    int
	wantAuth   string
	failCables bool
	failDelete bool
	failTags   bool
	failIface  string
	writes     []string
	auth       []string
}

func newFakeNetbox(t *testing.T, ifaces []map[string]any) *fakeNetbox {
	t.Helper()
	f := &fakeNetbox{devices: map[int]string{}, ifaces: ifaces, nextID: 1000, maxPage: 1, wantAuth: "Token " + netboxSecret}
	for _, i := range ifaces {
		d, ok := i["device"].(map[string]any)
		if !ok {
			t.Fatal("interface without device")
		}
		id, _ := d["id"].(int)
		name, _ := d["name"].(string)
		f.devices[id] = name
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func iface(id, device int, deviceName, name, typ, mac string) map[string]any {
	i := map[string]any{
		"id":     id,
		"name":   name,
		"device": map[string]any{"id": device, "name": deviceName},
		"type":   map[string]any{"value": typ},
		"cable":  nil,
	}
	if mac != "" {
		i["primary_mac_address"] = map[string]any{"mac_address": strings.ToUpper(mac)}
		i["mac_addresses"] = []any{map[string]any{"mac_address": strings.ToUpper(mac)}}
	}
	return i
}

func (f *fakeNetbox) addCable(id int, a, b int, tag string) {
	if tag != "" && f.tagByID(0, tag) == nil {
		f.nextID++
		f.addTag(f.nextID, tag)
	}
	c := map[string]any{
		"id":             id,
		"a_terminations": []any{map[string]any{"object_type": "dcim.interface", "object_id": a}},
		"b_terminations": []any{map[string]any{"object_type": "dcim.interface", "object_id": b}},
		"tags":           []any{},
	}
	if tag != "" {
		c["tags"] = []any{map[string]any{"slug": tag}}
	}
	f.cables = append(f.cables, c)
	for _, i := range f.ifaces {
		if i["id"] == a || i["id"] == b {
			i["cable"] = map[string]any{"id": id}
		}
	}
}

func (f *fakeNetbox) tagByID(id int, slug string) map[string]any {
	for _, t := range f.tags {
		if (slug != "" && t["slug"] == slug) || (id != 0 && t["id"] == id) {
			return t
		}
	}
	return nil
}

func (f *fakeNetbox) addTag(id int, slug string) {
	f.tags = append(f.tags, map[string]any{"id": id, "slug": slug, "name": slug})
}

func (f *fakeNetbox) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	}
	if r.Header.Get("Authorization") != f.wantAuth {
		writeJSON(w, http.StatusForbidden, map[string]string{"detail": "Invalid token"})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/dcim/interfaces/":
		f.list(w, r, f.ifaces)
	case r.Method == http.MethodGet && r.URL.Path == "/api/dcim/cables/":
		if f.tagByID(0, r.URL.Query().Get("tag")) == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"tag": []string{"Select a valid choice. That choice is not one of the available choices."}})
			return
		}
		var rows []map[string]any
		for _, c := range f.cables {
			if hasTag(c, r.URL.Query().Get("tag")) {
				rows = append(rows, c)
			}
		}
		f.list(w, r, rows)
	case r.Method == http.MethodGet && r.URL.Path == "/api/extras/tags/":
		var rows []map[string]any
		for _, t := range f.tags {
			if t["slug"] == r.URL.Query().Get("slug") {
				rows = append(rows, t)
			}
		}
		f.list(w, r, rows)
	case r.Method == http.MethodPost && r.URL.Path == "/api/extras/tags/":
		if f.failTags {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "no tags today"})
			return
		}
		var body map[string]any
		f.decode(r, &body)
		f.nextID++
		t := map[string]any{"id": f.nextID, "slug": body["slug"], "name": body["name"]}
		f.tags = append(f.tags, t)
		writeJSON(w, http.StatusCreated, t)
	case r.Method == http.MethodPost && r.URL.Path == "/api/dcim/interfaces/":
		f.createInterface(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/dcim/cables/":
		f.createCable(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/dcim/cables/"):
		f.deleteCable(w, r)
	default:
		http.NotFound(w, r)
	}
}

func hasTag(c map[string]any, slug string) bool {
	tags, _ := c["tags"].([]any)
	for _, t := range tags {
		if m, ok := t.(map[string]any); ok && m["slug"] == slug {
			return true
		}
	}
	return false
}

func (f *fakeNetbox) decode(r *http.Request, v any) {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r.Body)
	_ = json.Unmarshal(buf.Bytes(), v)
}

func (f *fakeNetbox) list(w http.ResponseWriter, r *http.Request, rows []map[string]any) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit == 0 || limit > f.maxPage {
		limit = f.maxPage
	}
	end := min(offset+limit, len(rows))
	var out []map[string]any
	if offset < len(rows) {
		out = rows[offset:end]
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(rows), "results": out})
}

func (f *fakeNetbox) createInterface(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Device int    `json:"device"`
		Name   string `json:"name"`
		Type   string `json:"type"`
	}
	f.decode(r, &body)
	if body.Name == f.failIface {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "refused"})
		return
	}
	f.nextID++
	f.ifaces = append(f.ifaces, iface(f.nextID, body.Device, f.devices[body.Device], body.Name, body.Type, ""))
	writeJSON(w, http.StatusCreated, map[string]any{"id": f.nextID})
}

func (f *fakeNetbox) createCable(w http.ResponseWriter, r *http.Request) {
	if f.failCables {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "boom"})
		return
	}
	var body struct {
		A    []termination `json:"a_terminations"`
		B    []termination `json:"b_terminations"`
		Tags []nbRef       `json:"tags"`
	}
	f.decode(r, &body)
	f.nextID++
	var tag string
	for _, t := range f.tags {
		if len(body.Tags) == 1 && t["id"] == body.Tags[0].ID {
			tag, _ = t["slug"].(string)
		}
	}
	f.addCable(f.nextID, body.A[0].ObjectID, body.B[0].ObjectID, tag)
	writeJSON(w, http.StatusCreated, map[string]any{"id": f.nextID})
}

func (f *fakeNetbox) deleteCable(w http.ResponseWriter, r *http.Request) {
	if f.failDelete {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "locked"})
		return
	}
	id, _ := strconv.Atoi(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/dcim/cables/"), "/"))
	var kept []map[string]any
	for _, c := range f.cables {
		if c["id"] == id {
			continue
		}
		kept = append(kept, c)
	}
	f.cables = kept
	for _, i := range f.ifaces {
		if ref, ok := i["cable"].(map[string]any); ok && ref["id"] == id {
			i["cable"] = nil
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeNetbox) ifaceByName(device, name string) map[string]any {
	for _, i := range f.ifaces {
		d, _ := i["device"].(map[string]any)
		if d["name"] == device && i["name"] == name {
			return i
		}
	}
	return nil
}

func lldp(chassis string, idx int, name, portID string) map[string]any {
	return map[string]any{
		"chassis_id": chassis, "local_port_idx": idx, "local_port_name": name,
		"port_id": portID, "port_descr": portID, "is_wired": true,
	}
}

func uport(idx int, name, media string) map[string]any {
	return map[string]any{"port_idx": idx, "name": name, "media": media}
}

func wired(mac, name, sw string, port int) map[string]any {
	return map[string]any{"mac": mac, "name": name, "is_wired": true, "sw_mac": sw, "sw_port": port}
}

func baseDevices() []any {
	return []any{
		map[string]any{
			"mac": macSW1, "name": "sw1", "state": 1,
			"port_table": []any{uport(1, "Port 1", "GE"), uport(2, "Port 2", "GE"), uport(25, "SFP+ 1", "SFP+")},
			"lldp_table": []any{lldp(macGW, 25, "eth25", "Port 5")},
		},
		map[string]any{
			"mac": macGW, "name": "gw", "state": 1,
			"port_table": []any{uport(5, "Port 5", "GE"), uport(6, "Port 6", "GE")},
			"lldp_table": []any{lldp(macSW1, 5, "eth5", "SFP_ 1")},
		},
	}
}

func baseClients() []any {
	return []any{
		wired(macPi, "pi", macSW1, 1),
		map[string]any{"mac": "de:ad:be:ef:00:01", "name": "phone", "is_wired": false},
	}
}

func baseIfaces() []map[string]any {
	return []map[string]any{
		iface(1, 10, "sw1", "mgmt", "virtual", macSW1),
		iface(2, 11, "gw", "mgmt", "1000base-t", macGW),
		iface(3, 11, "gw", "Port 5", "1000base-t", ""),
		iface(4, 12, "pi", "eth0", "1000base-t", macPi),
	}
}

type runResult struct {
	code        int
	out, errOut string
}

func (f *fakeNetbox) run(t *testing.T, u *fakeUnifi, extra ...string) runResult {
	t.Helper()
	return runWith(t, map[string]string{
		"UNIFI_URL": u.srv.URL, "UNIFI_API_KEY": unifiKey,
		"NETBOX_URL": f.srv.URL, "NETBOX_TOKEN": netboxSecret,
	}, extra...)
}

func runWith(t *testing.T, env map[string]string, args ...string) runResult {
	t.Helper()
	return runClient(t, env, &http.Client{}, args...)
}

func runClient(t *testing.T, env map[string]string, client *http.Client, args ...string) runResult {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(args, Env{
		Getenv: func(k string) string { return env[k] },
		Stdout: &out,
		Stderr: &errOut,
		Client: client,
	})
	return runResult{code: code, out: out.String(), errOut: errOut.String()}
}

func (r runResult) lines() map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(r.out, "\n") {
		m[l] = true
	}
	return m
}

func (r runResult) noSecrets(t *testing.T) {
	t.Helper()
	for _, s := range []string{unifiKey, netboxSecret} {
		if strings.Contains(r.out, s) || strings.Contains(r.errOut, s) {
			t.Errorf("output holds a secret:\n%s\n%s", r.out, r.errOut)
		}
	}
}

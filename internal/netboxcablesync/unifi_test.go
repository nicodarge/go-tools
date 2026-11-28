package netboxcablesync

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func roundTrip(t *testing.T, in, out any) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func decode[T any](t *testing.T, rows []any) []T {
	t.Helper()
	var out []T
	for _, r := range rows {
		var v T
		roundTrip(t, r, &v)
		out = append(out, v)
	}
	return out
}

func linkNames(links []unifiLink) []string {
	var out []string
	for _, l := range links {
		out = append(out, l.a.label+":"+l.a.port.name+" <-> "+l.b.label+":"+l.b.port.name)
	}
	return out
}

func withField(devices []any, idx int, key string, value any) []any {
	out := append([]any(nil), devices...)
	d, _ := out[idx].(map[string]any)
	cp := map[string]any{}
	for k, v := range d {
		cp[k] = v
	}
	cp[key] = value
	out[idx] = cp
	return out
}

func withLLDP(devices []any, idx int, entries ...any) []any {
	return withField(devices, idx, "lldp_table", entries)
}

func TestDeriveLinks(t *testing.T) {
	notWired := lldp(macGW, 25, "eth25", "Port 5")
	notWired["is_wired"] = false
	const (
		clientLink = "sw1:Port 1 <-> pi:"
		uplinkLink = "sw1:SFP+ 1 <-> gw:Port 5"
	)
	tests := []struct {
		name         string
		devices      []any
		clients      []any
		want         []string
		wantProblems []string
	}{
		{
			name:    "an LLDP link seen from both sides counts once",
			devices: baseDevices(),
			clients: baseClients(),
			want:    []string{clientLink, uplinkLink},
		},
		{
			name:    "an LLDP link seen from one side only",
			devices: withLLDP(baseDevices(), 1),
			want:    []string{uplinkLink},
		},
		{
			name:    "the peer port comes from the neighbour's own report when port_id is a MAC",
			devices: withLLDP(withLLDP(baseDevices(), 0, lldp(macGW, 25, "eth25", "00:00:00:00:00:99")), 1, lldp(macSW1, 5, "eth5", "00:00:00:00:00:98")),
			want:    []string{uplinkLink},
		},
		{
			name:         "a MAC port_id with no report from the neighbour is unresolved",
			devices:      withLLDP(withLLDP(baseDevices(), 0, lldp(macGW, 25, "eth25", "00:00:00:00:00:99")), 1),
			wantProblems: []string{"port of neighbour gw cannot be resolved"},
		},
		{
			name:    "a MAC port_id names the only port of a one-port device",
			devices: withLLDP(withField(withLLDP(baseDevices(), 0, lldp(macGW, 25, "eth25", "00:00:00:00:00:99")), 1, "port_table", []any{uport(5, "Port 5", "GE")}), 1),
			want:    []string{uplinkLink},
		},
		{
			name:    "a port the table does not list keeps the LLDP name",
			devices: withLLDP(withLLDP(baseDevices(), 0, lldp(macGW, 26, "eth26", "Port 5")), 1),
			want:    []string{"sw1:eth26 <-> gw:Port 5"},
		},
		{
			name:    "a non-wired entry is skipped",
			devices: withLLDP(withLLDP(baseDevices(), 0, notWired), 1),
		},
		{
			name:    "a neighbour outside UniFi is kept with its announced port",
			devices: withLLDP(withLLDP(baseDevices(), 0, lldp("00:00:00:00:09:09", 2, "eth2", "Gi1/0/3")), 1),
			want:    []string{"sw1:Port 2 <-> 00:00:00:00:09:09:Gi1/0/3"},
		},
		{
			name:    "an address of the ethernet table identifies a device",
			devices: withLLDP(withField(baseDevices(), 0, "ethernet_table", []any{map[string]any{"mac": "00:00:00:00:00:a1"}}), 1, lldp("00:00:00:00:00:a1", 5, "eth5", "SFP_ 1")),
			want:    []string{uplinkLink},
		},
		{
			name:         "a client behind a switch UniFi does not list",
			devices:      withLLDP(withLLDP(baseDevices(), 0), 1),
			clients:      []any{wired(macPi, "pi", "00:00:00:00:09:09", 1)},
			wantProblems: []string{"switch 00:00:00:00:09:09 is not a UniFi device"},
		},
		{
			name:         "a client with no port",
			devices:      withLLDP(withLLDP(baseDevices(), 0), 1),
			clients:      []any{wired(macPi, "pi", macSW1, 0)},
			wantProblems: []string{"no switch port reported on sw1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links, problems := deriveLinks(decode[unifiDevice](t, tt.devices), decode[unifiClient](t, tt.clients))
			if got := linkNames(links); !reflect.DeepEqual(got, tt.want) && (len(got) != 0 || len(tt.want) != 0) {
				t.Errorf("links %q, want %q", got, tt.want)
			}
			if len(problems) != len(tt.wantProblems) {
				t.Fatalf("problems %q, want %q", problems, tt.wantProblems)
			}
			for i, p := range problems {
				if !strings.Contains(p, tt.wantProblems[i]) {
					t.Errorf("problem %q lacks %q", p, tt.wantProblems[i])
				}
			}
		})
	}
}

func TestInterfaceType(t *testing.T) {
	tests := map[string]string{
		"GE": "1000base-t", "ge": "1000base-t", "2.5GE": "2.5gbase-t", "5GE": "5gbase-t", "10GE": "10gbase-t",
		"SFP": "1000base-x-sfp", "SFP+": "10gbase-x-sfpp", " SFP+ ": "10gbase-x-sfpp", "SFP28": "25gbase-x-sfp28",
		"QSFP+": "40gbase-x-qsfpp", "QSFP28": "100gbase-x-qsfp28", "": "other", "MoCA": "other",
	}
	for media, want := range tests {
		if got := interfaceType(media); got != want {
			t.Errorf("interfaceType(%q) = %q, want %q", media, got, want)
		}
	}
}

func TestPortFromID(t *testing.T) {
	d := unifiDevice{Ports: []unifiPort{{Idx: 1, Name: "Port 1", Media: "GE"}, {Idx: 25, Name: "SFP+ 1", Media: "SFP+"}}}
	tests := []struct {
		id   string
		want string
		ok   bool
	}{
		{"Port 1", "Port 1", true},
		{"port 1", "Port 1", true},
		{"Port1", "Port 1", true},
		{"SFP+ 1", "SFP+ 1", true},
		{"SFP_ 1", "SFP+ 1", true},
		{"Port 7", "", false},
		{"00:11:22:33:44:55", "", false},
		{"", "", false},
		{"Gi1/0/1", "", false},
	}
	for _, tt := range tests {
		p, ok := d.portFromID(tt.id)
		if ok != tt.ok || p.name != tt.want {
			t.Errorf("portFromID(%q) = %q, %v; want %q, %v", tt.id, p.name, ok, tt.want, tt.ok)
		}
	}
	if p, _ := d.portFromID("SFP_ 1"); p.ptype != "10gbase-x-sfpp" {
		t.Errorf("type %q", p.ptype)
	}
}

func TestNormMAC(t *testing.T) {
	tests := map[string]string{
		"AA:BB:CC:00:00:01":       "aa:bb:cc:00:00:01",
		"aa-bb-cc-00-00-01":       "aa:bb:cc:00:00:01",
		" aa:bb:cc:00:00:01 ":     "aa:bb:cc:00:00:01",
		"Port 5":                  "",
		"":                        "",
		"00:11:22:33:44:55:66:77": "",
	}
	for in, want := range tests {
		if got := normMAC(in); got != want {
			t.Errorf("normMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientInterfaceSkipsVirtualInterfaces(t *testing.T) {
	x := newIndex([]nbInterface{
		{ID: 1, Name: "eth0", Type: nbType{Value: "1000base-t"}, MACs: []nbMAC{{MACAddress: "AA:BB:CC:00:00:01"}}},
		{ID: 2, Name: "br0", Type: nbType{Value: "bridge"}, MACs: []nbMAC{{MACAddress: "AA:BB:CC:00:00:01"}}},
		{ID: 3, Name: "br1", Type: nbType{Value: "bridge"}, PrimaryMAC: &nbMAC{MACAddress: "AA:BB:CC:00:00:02"}},
	}, nil)
	if i, why := x.clientInterface("aa:bb:cc:00:00:01"); i == nil || i.ID != 1 {
		t.Errorf("the physical interface must win: %v %q", i, why)
	}
	if i, why := x.clientInterface("aa:bb:cc:00:00:02"); i != nil || !strings.Contains(why, "virtual") {
		t.Errorf("a bridge is not a cable end: %v %q", i, why)
	}
	if i, why := x.clientInterface("aa:bb:cc:00:00:03"); i != nil || !strings.Contains(why, "no NetBox interface") {
		t.Errorf("unknown MAC: %v %q", i, why)
	}
}

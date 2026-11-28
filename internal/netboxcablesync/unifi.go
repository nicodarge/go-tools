package netboxcablesync

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type unifiDevice struct {
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Ethernet []struct {
		MAC string `json:"mac"`
	} `json:"ethernet_table"`
	Ports []unifiPort `json:"port_table"`
	LLDP  []unifiLLDP `json:"lldp_table"`
}

type unifiPort struct {
	Idx   int    `json:"port_idx"`
	Name  string `json:"name"`
	Media string `json:"media"`
}

type unifiLLDP struct {
	ChassisID     string `json:"chassis_id"`
	LocalPortIdx  int    `json:"local_port_idx"`
	LocalPortName string `json:"local_port_name"`
	PortID        string `json:"port_id"`
	PortDescr     string `json:"port_descr"`
	IsWired       *bool  `json:"is_wired"`
}

type unifiClient struct {
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	IsWired  bool   `json:"is_wired"`
	SwMAC    string `json:"sw_mac"`
	SwPort   int    `json:"sw_port"`
}

type unifiEnvelope[T any] struct {
	Data []T `json:"data"`
}

func fetchUnifi[T any](ctx context.Context, c *apiClient, apiPath, site, what string) ([]T, error) {
	var env unifiEnvelope[T]
	path := apiPath + "/api/s/" + url.PathEscape(site) + "/stat/" + what
	if err := c.do(ctx, http.MethodGet, path, nil, &env, http.StatusOK); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// normMAC returns the lowercase colon form of an EUI-48 address, or "" when s is not one.
func normMAC(s string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return ""
	}
	return hw.String()
}

// mediaTypes maps the media UniFi reports for a port to a NetBox interface type.
var mediaTypes = map[string]string{
	"GE":     "1000base-t",
	"2.5GE":  "2.5gbase-t",
	"5GE":    "5gbase-t",
	"10GE":   "10gbase-t",
	"SFP":    "1000base-x-sfp",
	"SFP+":   "10gbase-x-sfpp",
	"SFP28":  "25gbase-x-sfp28",
	"QSFP+":  "40gbase-x-qsfpp",
	"QSFP28": "100gbase-x-qsfp28",
}

func interfaceType(media string) string {
	if t, ok := mediaTypes[strings.ToUpper(strings.TrimSpace(media))]; ok {
		return t
	}
	return "other"
}

var portNumber = regexp.MustCompile(`^(?i)port\s*(\d+)$`)

// portKey makes the spellings LLDP gives a port comparable: case, spaces and the "_" some firmware
// sends in place of "+" are ignored.
func portKey(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "+"))
	return strings.ReplaceAll(s, " ", "")
}

// port is one end of a link on a UniFi device: its NetBox interface name and type.
type port struct {
	name  string
	ptype string
}

// portByIdx names a port the way the controller does (port_table[].name); a port the table does not
// list keeps the LLDP name, else "Port <index>".
func (d *unifiDevice) portByIdx(idx int, fallback string) (port, bool) {
	for _, p := range d.Ports {
		if p.Idx == idx && idx != 0 {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				name = "Port " + strconv.Itoa(idx)
			}
			return port{name: name, ptype: interfaceType(p.Media)}, true
		}
	}
	if name := strings.TrimSpace(fallback); name != "" {
		return port{name: name, ptype: "other"}, true
	}
	if idx != 0 {
		return port{name: "Port " + strconv.Itoa(idx), ptype: "other"}, true
	}
	return port{}, false
}

// portFromID resolves the peer port LLDP announces (port_id, then port_descr) against this device's
// port table. A MAC is the port's own address, which names no port unless the table has one.
func (d *unifiDevice) portFromID(candidates ...string) (port, bool) {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if normMAC(c) != "" {
			if len(d.Ports) == 1 {
				return d.portByIdx(d.Ports[0].Idx, "")
			}
			continue
		}
		key := portKey(c)
		for _, p := range d.Ports {
			if portKey(p.Name) == key {
				return d.portByIdx(p.Idx, "")
			}
		}
		if m := portNumber.FindStringSubmatch(c); m != nil {
			if idx, err := strconv.Atoi(m[1]); err == nil {
				for _, p := range d.Ports {
					if p.Idx == idx {
						return d.portByIdx(idx, "")
					}
				}
			}
		}
	}
	return port{}, false
}

func (d *unifiDevice) label() string {
	if d.Name != "" {
		return d.Name
	}
	return normMAC(d.MAC)
}

// unifiEnd is one end of a derived link: a port of a device, or a client NIC.
type unifiEnd struct {
	macs   []string
	port   port
	client bool
	label  string
}

type unifiLink struct {
	a, b   unifiEnd
	source string
}

func (l unifiLink) key() string {
	a, b := l.a.key(), l.b.key()
	if b < a {
		a, b = b, a
	}
	return a + "|" + b
}

func (e unifiEnd) String() string {
	if e.client {
		return e.label
	}
	return e.label + ":" + e.port.name
}

func (e unifiEnd) key() string {
	if e.client {
		return "client:" + e.macs[0]
	}
	return "port:" + e.macs[0] + "/" + e.port.name
}

func deviceMACs(d *unifiDevice) []string {
	var macs []string
	seen := map[string]bool{}
	add := func(raw string) {
		if m := normMAC(raw); m != "" && !seen[m] {
			seen[m] = true
			macs = append(macs, m)
		}
	}
	add(d.MAC)
	for _, e := range d.Ethernet {
		add(e.MAC)
	}
	return macs
}

func (d *unifiDevice) portEnd(p port) unifiEnd {
	return unifiEnd{macs: deviceMACs(d), port: p, label: d.label()}
}

// deriveLinks turns the controller's view into the desired link set: LLDP neighbours, seen from
// both sides and counted once, then wired clients on their switch port. What cannot be turned into
// a link is returned as a problem line.
func deriveLinks(devices []unifiDevice, clients []unifiClient) ([]unifiLink, []string) {
	byMAC := map[string]*unifiDevice{}
	for i := range devices {
		for _, m := range deviceMACs(&devices[i]) {
			byMAC[m] = &devices[i]
		}
	}
	var links []unifiLink
	var problems []string
	seen := map[string]bool{}
	add := func(l unifiLink) {
		if k := l.key(); !seen[k] {
			seen[k] = true
			links = append(links, l)
		}
	}
	for i := range devices {
		a := &devices[i]
		if len(deviceMACs(a)) == 0 {
			continue
		}
		for _, e := range a.LLDP {
			if e.IsWired != nil && !*e.IsWired {
				continue
			}
			l, problem := lldpLink(a, e, byMAC)
			if problem != "" {
				problems = append(problems, problem)
				continue
			}
			if l != nil {
				add(*l)
			}
		}
	}
	for _, c := range clients {
		if !c.IsWired || c.SwMAC == "" {
			continue
		}
		cm := normMAC(c.MAC)
		label := firstNonEmpty(c.Name, c.Hostname, cm)
		if cm == "" {
			problems = append(problems, fmt.Sprintf("client %q: not a MAC address", label))
			continue
		}
		sw := byMAC[normMAC(c.SwMAC)]
		if sw == nil {
			problems = append(problems, fmt.Sprintf("client %s (%s): switch %s is not a UniFi device", label, cm, c.SwMAC))
			continue
		}
		p, ok := sw.portByIdx(c.SwPort, "")
		if !ok {
			problems = append(problems, fmt.Sprintf("client %s (%s): no switch port reported on %s", label, cm, sw.label()))
			continue
		}
		add(unifiLink{
			a:      sw.portEnd(p),
			b:      unifiEnd{macs: []string{cm}, client: true, label: label},
			source: "client",
		})
	}
	sort.Slice(links, func(i, j int) bool { return links[i].key() < links[j].key() })
	sort.Strings(problems)
	return links, problems
}

func lldpLink(a *unifiDevice, e unifiLLDP, byMAC map[string]*unifiDevice) (*unifiLink, string) {
	local, ok := a.portByIdx(e.LocalPortIdx, e.LocalPortName)
	if !ok {
		return nil, fmt.Sprintf("lldp on %s: neighbour %s has no local port", a.label(), e.ChassisID)
	}
	chassis := normMAC(e.ChassisID)
	if chassis == "" {
		return nil, fmt.Sprintf("lldp on %s %s: neighbour chassis %q is not a MAC address", a.label(), local.name, e.ChassisID)
	}
	peer := byMAC[chassis]
	if peer == a {
		return nil, ""
	}
	if peer == nil {
		for _, c := range []string{e.PortID, e.PortDescr} {
			if c = strings.TrimSpace(c); c != "" && normMAC(c) == "" {
				return &unifiLink{
					a:      a.portEnd(local),
					b:      unifiEnd{macs: []string{chassis}, port: port{name: c, ptype: "other"}, label: chassis},
					source: "lldp",
				}, ""
			}
		}
		return nil, fmt.Sprintf("lldp on %s %s: neighbour %s is not a UniFi device and announces no port name", a.label(), local.name, chassis)
	}
	remote, ok := peer.portFromID(e.PortID, e.PortDescr)
	if !ok {
		remote, ok = reversePort(a, peer, byMAC)
	}
	if !ok {
		return nil, fmt.Sprintf("lldp on %s %s: port of neighbour %s cannot be resolved (%q)", a.label(), local.name, peer.label(), e.PortID)
	}
	return &unifiLink{a: a.portEnd(local), b: peer.portEnd(remote), source: "lldp"}, ""
}

// reversePort is the port peer reports for its one neighbour a, when the port a announced names none.
func reversePort(a, peer *unifiDevice, byMAC map[string]*unifiDevice) (port, bool) {
	var found []port
	for _, e := range peer.LLDP {
		if e.IsWired != nil && !*e.IsWired {
			continue
		}
		if byMAC[normMAC(e.ChassisID)] != a {
			continue
		}
		if p, ok := peer.portByIdx(e.LocalPortIdx, e.LocalPortName); ok {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return port{}, false
	}
	return found[0], true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

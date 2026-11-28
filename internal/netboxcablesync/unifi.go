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
	"unicode"
)

type unifiDevice struct {
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	State    int    `json:"state"`
	Ethernet []struct {
		MAC string `json:"mac"`
	} `json:"ethernet_table"`
	Ports []unifiPort `json:"port_table"`
	LLDP  []unifiLLDP `json:"lldp_table"`

	scheme   nameScheme
	defaults map[int]string
}

// nameScheme is how a device's default port names are told: SFP-class ports numbered by port_idx
// like the copper ones, or by their rank among ports of their media. schemeUnknown is a device whose
// every SFP-class port is renamed, so that neither can be told.
type nameScheme int

const (
	schemeIdx nameScheme = iota
	schemeRank
	schemeUnknown
)

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
	"2P5GE":  "2.5gbase-t",
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

// port is one end of a link on a UniFi device: its NetBox interface name, the label the controller
// gives it when that is not the name, and the interface type.
type port struct {
	name  string
	label string
	ptype string
}

// mediaPrefixes are the port name prefixes UniFi's default scheme derives from the media; any other
// media is copper, named by its port_idx.
var mediaPrefixes = map[string]string{
	"SFP":    "SFP",
	"SFP+":   "SFP+",
	"SFP28":  "SFP28",
	"QSFP+":  "QSFP+",
	"QSFP28": "QSFP28",
}

func mediaPrefix(media string) (string, bool) {
	prefix, ok := mediaPrefixes[strings.ToUpper(strings.TrimSpace(media))]
	return prefix, ok
}

// rankName is "<prefix> <n>", n being the rank of an SFP-class port among the device's ports of its
// prefix by port_idx.
func (d *unifiDevice) rankName(p unifiPort) (string, bool) {
	prefix, ok := mediaPrefix(p.Media)
	if !ok {
		return "", false
	}
	rank := 1
	for _, o := range d.Ports {
		if other, same := mediaPrefix(o.Media); same && other == prefix && o.Idx < p.Idx {
			rank++
		}
	}
	return prefix + " " + strconv.Itoa(rank), true
}

// classify decides the device's naming scheme from positive evidence: an SFP-class port named
// "Port <its idx>" means idx names, one named by its rank means rank names, and none (or both) means
// the names cannot be told. It records the default name of every port the scheme names.
func (d *unifiDevice) classify() {
	var sfp, byIdx, byRank bool
	ranks := map[int]string{}
	for _, p := range d.Ports {
		rn, ok := d.rankName(p)
		if !ok {
			continue
		}
		sfp = true
		ranks[p.Idx] = rn
		switch strings.TrimSpace(p.Name) {
		case "Port " + strconv.Itoa(p.Idx):
			byIdx = true
		case rn:
			byRank = true
		}
	}
	d.scheme = schemeIdx
	switch {
	case !sfp, byIdx && !byRank:
	case byRank && !byIdx:
		d.scheme = schemeRank
	default:
		d.scheme = schemeUnknown
	}
	d.defaults = map[int]string{}
	for _, p := range d.Ports {
		_, isSFP := ranks[p.Idx]
		switch {
		case !isSFP, d.scheme == schemeIdx:
			d.defaults[p.Idx] = "Port " + strconv.Itoa(p.Idx)
		case d.scheme == schemeRank:
			d.defaults[p.Idx] = ranks[p.Idx]
		}
	}
}

// defaultName is the name UniFi gives a port until it is renamed, if the scheme tells it.
func (d *unifiDevice) defaultName(p unifiPort) (string, bool) {
	name, ok := d.defaults[p.Idx]
	return name, ok
}

// portByIdx names a port by UniFi's default name, so that renaming it in UniFi changes only its
// label. A port the table lacks keeps the LLDP name, else "Port <index>".
func (d *unifiDevice) portByIdx(idx int, fallback string) (port, bool) {
	for _, p := range d.Ports {
		if p.Idx == idx && idx != 0 {
			name, ok := d.defaultName(p)
			if !ok {
				return port{}, false
			}
			label := printable(p.Name, maxLabel)
			if label == name {
				label = ""
			}
			return port{name: name, label: label, ptype: interfaceType(p.Media)}, true
		}
	}
	if name := printable(strings.ReplaceAll(fallback, "_", "+"), maxLabel); name != "" {
		return port{name: name, ptype: "other"}, true
	}
	if idx != 0 {
		return port{name: "Port " + strconv.Itoa(idx), ptype: "other"}, true
	}
	return port{}, false
}

const (
	maxText  = 200
	maxLabel = 64
)

// printable makes text from the network safe to print and to store: whitespace becomes one space,
// anything unprintable is dropped, and at most limit runes are kept.
func printable(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsPrint(r):
			return r
		case unicode.IsSpace(r):
			return ' '
		}
		return -1
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > limit {
		return string(runes[:limit])
	}
	return s
}

func (d *unifiDevice) sanitize() {
	d.MAC = printable(d.MAC, maxText)
	d.Name = printable(d.Name, maxText)
	for i := range d.Ethernet {
		d.Ethernet[i].MAC = printable(d.Ethernet[i].MAC, maxText)
	}
	for i := range d.Ports {
		d.Ports[i].Name = printable(d.Ports[i].Name, maxText)
		d.Ports[i].Media = printable(d.Ports[i].Media, maxText)
	}
	d.classify()
	for i := range d.LLDP {
		e := &d.LLDP[i]
		e.ChassisID = printable(e.ChassisID, maxText)
		e.LocalPortName = printable(e.LocalPortName, maxText)
		e.PortID = printable(e.PortID, maxText)
		e.PortDescr = printable(e.PortDescr, maxText)
	}
}

func (c *unifiClient) sanitize() {
	c.MAC = printable(c.MAC, maxText)
	c.Name = printable(c.Name, maxText)
	c.Hostname = printable(c.Hostname, maxText)
	c.SwMAC = printable(c.SwMAC, maxText)
}

// cleanPortID drops the "local " prefix some firmware puts before the port name.
func cleanPortID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 6 && strings.EqualFold(s[:6], "local ") {
		s = strings.TrimSpace(s[6:])
	}
	return s
}

// portFromID resolves the port LLDP announces (port_id, then port_descr) against this device's port
// table. A MAC names a port only when the device has a single one.
func (d *unifiDevice) portFromID(candidates ...string) (port, bool) {
	for _, c := range candidates {
		c = cleanPortID(c)
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
			if name, ok := d.defaultName(p); ok && portKey(name) == key {
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
	for _, c := range candidates {
		if key := portKey(cleanPortID(c)); key != "" && normMAC(c) == "" {
			for _, p := range d.Ports {
				if portKey(p.Name) == key {
					return d.portByIdx(p.Idx, "")
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
	if m := normMAC(d.MAC); m != "" {
		return m
	}
	return d.MAC
}

// unifiEnd is one end of a derived link: a port of a UniFi device, a port of a neighbour UniFi does
// not manage, or a client NIC.
type unifiEnd struct {
	macs   []string
	port   port
	unifi  bool
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
	return unifiEnd{macs: deviceMACs(d), port: p, unifi: true, label: d.label()}
}

// scope names what a problem makes uncertain: one port of a device, or all of them when port is "".
type scope struct {
	macs []string
	port string
}

// problem is something UniFi reports that cannot become a link. Its scopes and clients are the
// NetBox interfaces whose cables the run must not judge.
type problem struct {
	msg     string
	scopes  []scope
	clients []string
}

type derived struct {
	links    []unifiLink
	problems []problem
	// seen holds every MAC positively seen on a port now: wired clients and LLDP neighbours.
	seen    map[string]bool
	devices [][]string
}

// locallyAdministered reports whether a MAC has the locally administered bit: virtual NICs, not
// hardware.
func locallyAdministered(mac string) bool {
	hw, err := net.ParseMAC(mac)
	return err == nil && len(hw) > 0 && hw[0]&2 != 0
}

// deriveLinks turns the controller's view into the desired link set: LLDP neighbours, seen from
// both sides and counted once, then wired clients on their switch port.
func deriveLinks(devices []unifiDevice, clients []unifiClient) derived {
	out := derived{seen: map[string]bool{}}
	for i := range devices {
		devices[i].sanitize()
	}
	for i := range clients {
		clients[i].sanitize()
	}
	byMAC := map[string]*unifiDevice{}
	owners := map[string][]*unifiDevice{}
	excluded := map[*unifiDevice]bool{}
	for i := range devices {
		d := &devices[i]
		macs := deviceMACs(d)
		if len(macs) == 0 {
			continue
		}
		out.devices = append(out.devices, macs)
		for _, m := range macs {
			owners[m] = append(owners[m], d)
		}
		if d.scheme == schemeUnknown {
			out.problems = append(out.problems, problem{
				msg:    fmt.Sprintf("device %s: every SFP port is renamed, so its ports cannot be named; they are left out and its cables kept out of --prune", d.label()),
				scopes: []scope{{macs: macs}},
			})
		}
		if d.State != 1 {
			out.problems = append(out.problems, problem{
				msg:    fmt.Sprintf("device %s is not connected (state %d): its ports are kept out of --prune", d.label(), d.State),
				scopes: []scope{{macs: macs}},
			})
		}
	}
	for m, ds := range owners {
		if len(ds) == 1 {
			byMAC[m] = ds[0]
			continue
		}
		p := problem{msg: fmt.Sprintf("several UniFi devices share MAC %s: left out", m)}
		for _, d := range ds {
			excluded[d] = true
			p.scopes = append(p.scopes, scope{macs: deviceMACs(d)})
		}
		out.problems = append(out.problems, p)
	}
	dup := map[string]bool{}
	add := func(l unifiLink) {
		if k := l.key(); !dup[k] {
			dup[k] = true
			out.links = append(out.links, l)
		}
	}
	for i := range devices {
		a := &devices[i]
		if len(deviceMACs(a)) == 0 || excluded[a] {
			continue
		}
		for _, e := range a.LLDP {
			if e.IsWired != nil && !*e.IsWired {
				continue
			}
			if m := normMAC(e.ChassisID); m != "" {
				out.seen[m] = true
			}
			l, pr := lldpLink(a, e, byMAC)
			if pr != nil {
				out.problems = append(out.problems, *pr)
			}
			if l != nil {
				add(*l)
			}
		}
	}
	skipped := 0
	for _, c := range clients {
		if !c.IsWired || c.SwMAC == "" {
			continue
		}
		cm := normMAC(c.MAC)
		label := firstNonEmpty(c.Name, c.Hostname, cm)
		if cm == "" {
			out.problems = append(out.problems, problem{msg: fmt.Sprintf("client %q: not a MAC address", label)})
			continue
		}
		out.seen[cm] = true
		if locallyAdministered(cm) {
			skipped++
			continue
		}
		sw := byMAC[normMAC(c.SwMAC)]
		if sw == nil && len(owners[normMAC(c.SwMAC)]) > 1 {
			out.problems = append(out.problems, problem{
				msg:     fmt.Sprintf("client %s (%s): several UniFi devices share MAC %s", label, cm, normMAC(c.SwMAC)),
				clients: []string{cm},
			})
			continue
		}
		if sw == nil {
			out.problems = append(out.problems, problem{
				msg:     fmt.Sprintf("client %s (%s): switch %s is not a UniFi device", label, cm, c.SwMAC),
				clients: []string{cm},
			})
			continue
		}
		p, ok := sw.portByIdx(c.SwPort, "")
		if !ok {
			out.problems = append(out.problems, problem{
				msg:     fmt.Sprintf("client %s (%s): no switch port reported on %s", label, cm, sw.label()),
				scopes:  []scope{{macs: deviceMACs(sw)}},
				clients: []string{cm},
			})
			continue
		}
		add(unifiLink{
			a:      sw.portEnd(p),
			b:      unifiEnd{macs: []string{cm}, client: true, label: label},
			source: "client",
		})
	}
	if skipped > 0 {
		out.problems = append(out.problems, problem{msg: fmt.Sprintf("%d wired client(s) with a locally administered MAC skipped", skipped)})
	}
	sort.Slice(out.links, func(i, j int) bool { return out.links[i].key() < out.links[j].key() })
	sort.Slice(out.problems, func(i, j int) bool { return out.problems[i].msg < out.problems[j].msg })
	return out
}

func lldpLink(a *unifiDevice, e unifiLLDP, byMAC map[string]*unifiDevice) (*unifiLink, *problem) {
	own := deviceMACs(a)
	local, ok := a.portByIdx(e.LocalPortIdx, e.LocalPortName)
	if !ok {
		return nil, &problem{
			msg:    fmt.Sprintf("lldp on %s: neighbour %s has no local port", a.label(), e.ChassisID),
			scopes: []scope{{macs: own}},
		}
	}
	mine := scope{macs: own, port: local.name}
	chassis := normMAC(e.ChassisID)
	if chassis == "" {
		return nil, &problem{
			msg:    fmt.Sprintf("lldp on %s %s: neighbour chassis %q is not a MAC address", a.label(), local.name, e.ChassisID),
			scopes: []scope{mine},
		}
	}
	peer := byMAC[chassis]
	if peer == a {
		return nil, nil
	}
	if peer == nil {
		for _, c := range []string{e.PortID, e.PortDescr} {
			if c = cleanPortID(c); c != "" && normMAC(c) == "" {
				return &unifiLink{
					a:      a.portEnd(local),
					b:      unifiEnd{macs: []string{chassis}, port: port{name: c, ptype: "other"}, label: chassis},
					source: "lldp",
				}, nil
			}
		}
		return nil, &problem{
			msg:    fmt.Sprintf("lldp on %s %s: neighbour %s is not a UniFi device and announces no port name", a.label(), local.name, chassis),
			scopes: []scope{mine},
		}
	}
	remote, ok := peer.portFromID(e.PortID, e.PortDescr)
	if !ok {
		remote, ok = reversePort(a, peer, byMAC)
	}
	if !ok {
		return nil, &problem{
			msg:    fmt.Sprintf("lldp on %s %s: port of neighbour %s cannot be resolved (%q)", a.label(), local.name, peer.label(), e.PortID),
			scopes: []scope{mine, {macs: deviceMACs(peer)}},
		}
	}
	return &unifiLink{a: a.portEnd(local), b: peer.portEnd(remote), source: "lldp"}, nil
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

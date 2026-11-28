package netboxcablesync

import (
	"fmt"
	"io"
	"sort"
)

type resolvedEnd struct {
	device deviceRef
	name   string
	label  string
	iface  *nbInterface
	ptype  string
}

func (e resolvedEnd) key() string { return nameKey(e.device.id, e.name) }

func (e resolvedEnd) String() string { return e.device.name + ":" + e.name }

// resolvedLink is a link between two NetBox interfaces. replaces holds the tagged cables to delete
// before it can be created.
type resolvedLink struct {
	a, b     resolvedEnd
	source   string
	replaces []int
}

func (l resolvedLink) String() string { return l.a.String() + " <-> " + l.b.String() }

func (l resolvedLink) pair() string {
	a, b := l.a.key(), l.b.key()
	if b < a {
		a, b = b, a
	}
	return a + "|" + b
}

type ifaceCreate struct {
	device deviceRef
	name   string
	label  string
	ptype  string
}

type cableDelete struct {
	id     int
	desc   string
	reason string
}

// plan is what a run would change, and what it refuses to touch.
type plan struct {
	interfaces []ifaceCreate
	creates    []resolvedLink
	deletes    []cableDelete
	stale      []string
	kept       []string
	conflicts  []string
	unmatched  []string
	ok         int
}

func (p *plan) writes() bool {
	return len(p.interfaces)+len(p.creates)+len(p.deletes) > 0
}

// resolveEnd finds the NetBox interface of a link end. Only the port of a UniFi device may be
// missing: it is created.
func resolveEnd(x *nbIndex, e unifiEnd) (resolvedEnd, string) {
	if e.client {
		i, why := x.clientInterface(e.macs[0])
		if why != "" {
			return resolvedEnd{}, why
		}
		return resolvedEnd{device: deviceRef{id: i.Device.ID, name: i.Device.Name}, name: i.Name, iface: i}, ""
	}
	d, why := x.device(e.macs)
	if why != "" {
		return resolvedEnd{}, why
	}
	i := x.byName[nameKey(d.id, e.port.name)]
	switch {
	case i == nil && !e.unifi:
		return resolvedEnd{}, fmt.Sprintf("no NetBox interface %s on %s", e.port.name, d.name)
	case i != nil && !i.connectable():
		return resolvedEnd{}, fmt.Sprintf("%s:%s cannot take a cable (virtual, bridge, LAG, wireless or marked connected)", d.name, i.Name)
	}
	return resolvedEnd{device: d, name: e.port.name, label: e.port.label, iface: i, ptype: e.port.ptype}, ""
}

type ownedCable struct {
	id   int
	a, b int
}

func ownedCables(x *nbIndex, tag string) map[int]*ownedCable {
	owned := map[int]*ownedCable{}
	for _, c := range x.cables {
		tagged := false
		for _, t := range c.Tags {
			if t.Slug == tag {
				tagged = true
			}
		}
		if !tagged || len(c.A) != 1 || len(c.B) != 1 {
			continue
		}
		if c.A[0].ObjectType != "dcim.interface" || c.B[0].ObjectType != "dcim.interface" {
			continue
		}
		oc := &ownedCable{id: c.ID, a: c.A[0].ObjectID, b: c.B[0].ObjectID}
		owned[oc.a] = oc
		owned[oc.b] = oc
	}
	return owned
}

func (x *nbIndex) describe(ifaceID int) string {
	if i := x.byID[ifaceID]; i != nil {
		return i.Device.Name + ":" + i.Name
	}
	return fmt.Sprintf("interface %d", ifaceID)
}

func (x *nbIndex) describeCable(c *ownedCable) string {
	return x.describe(c.a) + " <-> " + x.describe(c.b)
}

// protect marks the interfaces a problem makes uncertain, so --prune keeps their tagged cables.
func (x *nbIndex) protect(protected map[int]bool, pr problem) {
	for _, sc := range pr.scopes {
		d, why := x.device(sc.macs)
		if why != "" {
			continue
		}
		if sc.port == "" {
			for _, i := range x.byDevice[d.id] {
				protected[i.ID] = true
			}
		} else if i := x.byName[nameKey(d.id, sc.port)]; i != nil {
			protected[i.ID] = true
		}
	}
	for _, m := range pr.clients {
		for _, i := range x.byMAC[m] {
			protected[i.ID] = true
		}
	}
}

// farEndSeen reports whether every end of a cable that is not an interface of a UniFi device
// carries a MAC seen on a port now: an offline client leaves the controller's lists without being
// unplugged.
func (x *nbIndex) farEndSeen(uni map[int]bool, seen map[string]bool, oc *ownedCable) bool {
	for _, id := range []int{oc.a, oc.b} {
		i := x.byID[id]
		if i != nil && uni[i.Device.ID] {
			continue
		}
		found := false
		if i != nil {
			for _, m := range i.macs() {
				found = found || seen[m]
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// buildPlan compares the desired links with NetBox. Only cables carrying tag are ever planned for
// deletion, and only with prune.
func buildPlan(x *nbIndex, tag string, view derived, prune bool) *plan {
	p := &plan{}
	protected := map[int]bool{}
	protect := func(ends ...resolvedEnd) {
		for _, e := range ends {
			if e.iface != nil {
				protected[e.iface.ID] = true
			}
		}
	}
	for _, pr := range view.problems {
		if pr.msg != "" {
			p.unmatched = append(p.unmatched, pr.msg)
		}
		x.protect(protected, pr)
	}
	uni := map[int]bool{}
	for _, macs := range view.devices {
		if dev, why := x.device(macs); why == "" {
			uni[dev.id] = true
		}
	}

	var resolved []resolvedLink
	at := map[string]int{}
	for _, l := range view.links {
		a, whyA := resolveEnd(x, l.a)
		b, whyB := resolveEnd(x, l.b)
		if whyA != "" || whyB != "" {
			p.unmatched = append(p.unmatched, fmt.Sprintf("%s <-> %s (%s): %s", l.a, l.b, l.source, joinReasons(whyA, whyB)))
			protect(a, b)
			continue
		}
		rl := resolvedLink{a: a, b: b, source: l.source}
		if n, dup := at[rl.pair()]; dup {
			if rl.source == "lldp" {
				resolved[n] = rl
			}
			continue
		}
		at[rl.pair()] = len(resolved)
		resolved = append(resolved, rl)
	}

	uses := map[string]int{}
	for _, l := range resolved {
		uses[l.a.key()]++
		uses[l.b.key()]++
	}
	var candidates []resolvedLink
	for _, l := range resolved {
		switch {
		case uses[l.a.key()] > 1:
			p.conflicts = append(p.conflicts, fmt.Sprintf("%s (%s): %s carries several links, so no single cable can be derived", l, l.source, l.a))
			protect(l.a, l.b)
		case uses[l.b.key()] > 1:
			p.conflicts = append(p.conflicts, fmt.Sprintf("%s (%s): %s carries several links, so no single cable can be derived", l, l.source, l.b))
			protect(l.a, l.b)
		default:
			candidates = append(candidates, l)
		}
	}

	owned := ownedCables(x, tag)
	matched := map[int]bool{}
	var pending []resolvedLink
	for _, l := range candidates {
		if l.a.iface != nil && l.b.iface != nil && l.a.iface.Cable != nil && l.b.iface.Cable != nil && l.a.iface.Cable.ID == l.b.iface.Cable.ID {
			p.ok++
			matched[l.a.iface.Cable.ID] = true
			continue
		}
		pending = append(pending, l)
	}

	doomed := map[int]string{}
	for _, l := range pending {
		var blocks []string
		replaced := map[int]string{}
		for _, e := range []resolvedEnd{l.a, l.b} {
			if e.iface == nil || e.iface.Cable == nil {
				continue
			}
			id := e.iface.Cable.ID
			oc := owned[e.iface.ID]
			switch {
			case oc == nil:
				blocks = append(blocks, fmt.Sprintf("%s already has cable #%d, which this tool does not manage", e, id))
			case prune:
				replaced[id] = "replaced by " + l.String()
			default:
				blocks = append(blocks, fmt.Sprintf("%s has stale cable #%d (%s); --prune would replace it", e, id, x.describeCable(oc)))
			}
		}
		if len(blocks) > 0 {
			for _, b := range blocks {
				p.conflicts = append(p.conflicts, fmt.Sprintf("%s (%s): %s", l, l.source, b))
			}
			protect(l.a, l.b)
			continue
		}
		for id, why := range replaced {
			doomed[id] = why
			l.replaces = append(l.replaces, id)
		}
		sort.Ints(l.replaces)
		for _, e := range []resolvedEnd{l.a, l.b} {
			if e.iface == nil {
				p.interfaces = append(p.interfaces, ifaceCreate{device: e.device, name: e.name, label: e.label, ptype: e.ptype})
			}
		}
		p.creates = append(p.creates, l)
	}

	byID := map[int]*ownedCable{}
	var ids []int
	for _, oc := range owned {
		if byID[oc.id] == nil {
			byID[oc.id] = oc
			ids = append(ids, oc.id)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		if matched[id] {
			continue
		}
		oc := byID[id]
		desc := fmt.Sprintf("#%d %s", id, x.describeCable(oc))
		switch {
		case doomed[id] != "":
			p.deletes = append(p.deletes, cableDelete{id: id, desc: desc, reason: doomed[id]})
		case protected[oc.a] || protected[oc.b]:
			p.kept = append(p.kept, desc+": a port of it is on a link or device UniFi does not report reliably")
		case !x.farEndSeen(uni, view.seen, oc):
			p.kept = append(p.kept, desc+": an end UniFi does not manage was not seen on any port now")
		case prune:
			p.deletes = append(p.deletes, cableDelete{id: id, desc: desc, reason: "link no longer in UniFi"})
		default:
			p.stale = append(p.stale, desc+": link no longer in UniFi; --prune would delete it")
		}
	}
	return p
}

func joinReasons(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

func (p *plan) render(w io.Writer) {
	for _, i := range p.interfaces {
		if i.label != "" {
			_, _ = fmt.Fprintf(w, "create interface %s:%s (%s, label %q)\n", i.device.name, i.name, i.ptype, i.label)
			continue
		}
		_, _ = fmt.Fprintf(w, "create interface %s:%s (%s)\n", i.device.name, i.name, i.ptype)
	}
	for _, l := range p.creates {
		_, _ = fmt.Fprintf(w, "create cable %s (%s)\n", l, l.source)
	}
	for _, d := range p.deletes {
		_, _ = fmt.Fprintf(w, "delete cable %s (%s)\n", d.desc, d.reason)
	}
	for _, s := range p.stale {
		_, _ = fmt.Fprintf(w, "stale cable %s\n", s)
	}
	for _, s := range p.kept {
		_, _ = fmt.Fprintf(w, "kept cable %s\n", s)
	}
	for _, s := range p.conflicts {
		_, _ = fmt.Fprintf(w, "conflict %s\n", s)
	}
	for _, s := range p.unmatched {
		_, _ = fmt.Fprintf(w, "unmatched %s\n", s)
	}
	_, _ = fmt.Fprintf(w, "summary: %d cables in place, %d to create, %d to delete, %d interfaces to create, %d stale, %d kept, %d conflicts, %d unmatched\n",
		p.ok, len(p.creates), len(p.deletes), len(p.interfaces), len(p.stale), len(p.kept), len(p.conflicts), len(p.unmatched))
}

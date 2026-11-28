package netboxcablesync

import (
	"fmt"
	"io"
	"sort"
)

type resolvedEnd struct {
	device deviceRef
	name   string
	iface  *nbInterface
	ptype  string
}

func (e resolvedEnd) key() string { return nameKey(e.device.id, e.name) }

func (e resolvedEnd) String() string { return e.device.name + ":" + e.name }

type resolvedLink struct {
	a, b   resolvedEnd
	source string
}

func (l resolvedLink) String() string { return l.a.String() + " <-> " + l.b.String() }

type ifaceCreate struct {
	device deviceRef
	name   string
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
	if i != nil && unmanaged[i.Type.Value] {
		return resolvedEnd{}, fmt.Sprintf("%s:%s is a virtual, bridge or LAG interface", d.name, i.Name)
	}
	return resolvedEnd{device: d, name: e.port.name, iface: i, ptype: e.port.ptype}, ""
}

// ownedCable is a cable carrying the tag whose two terminations are one interface each.
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

// buildPlan compares the desired links with NetBox. Only cables carrying tag are ever planned for
// deletion, and only with prune.
func buildPlan(x *nbIndex, tag string, links []unifiLink, problems []string, prune bool) *plan {
	p := &plan{unmatched: append([]string(nil), problems...)}
	protected := map[int]bool{}
	protect := func(ends ...resolvedEnd) {
		for _, e := range ends {
			if e.iface != nil {
				protected[e.iface.ID] = true
			}
		}
	}

	var resolved []resolvedLink
	for _, l := range links {
		a, whyA := resolveEnd(x, l.a)
		b, whyB := resolveEnd(x, l.b)
		if whyA != "" || whyB != "" {
			p.unmatched = append(p.unmatched, fmt.Sprintf("%s <-> %s (%s): %s", l.a, l.b, l.source, joinReasons(whyA, whyB)))
			protect(a, b)
			continue
		}
		resolved = append(resolved, resolvedLink{a: a, b: b, source: l.source})
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
			case oc == nil || oc.id != id:
				blocks = append(blocks, fmt.Sprintf("%s already has cable #%d, which this tool does not manage", e, id))
			case matched[id]:
				blocks = append(blocks, fmt.Sprintf("%s is cabled by #%d for another link", e, id))
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
		}
		for _, e := range []resolvedEnd{l.a, l.b} {
			if e.iface == nil {
				p.interfaces = append(p.interfaces, ifaceCreate{device: e.device, name: e.name, ptype: e.ptype})
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
			p.kept = append(p.kept, desc+": a link on one of its ports could not be resolved, so its absence proves nothing")
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

// render prints the plan: one line per action or finding, then a summary.
func (p *plan) render(w io.Writer) {
	for _, i := range p.interfaces {
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
	_, _ = fmt.Fprintf(w, "summary: %d cables in place, %d to create, %d to delete, %d interfaces to create, %d stale, %d conflicts, %d unmatched\n",
		p.ok, len(p.creates), len(p.deletes), len(p.interfaces), len(p.stale), len(p.conflicts), len(p.unmatched))
}

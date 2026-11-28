package netboxcablesync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

type termination struct {
	ObjectType string `json:"object_type"`
	ObjectID   int    `json:"object_id"`
}

type cableBody struct {
	A      []termination `json:"a_terminations"`
	B      []termination `json:"b_terminations"`
	Status string        `json:"status"`
	Tags   []nbRef       `json:"tags"`
}

type interfaceBody struct {
	Device int    `json:"device"`
	Name   string `json:"name"`
	Label  string `json:"label,omitempty"`
	Type   string `json:"type"`
}

// applier writes a plan. Every failed write, and every cable skipped because a write it needs
// failed, counts once in failed; the remaining writes are still attempted.
type applier struct {
	ctx     context.Context
	c       *apiClient
	out     io.Writer
	errOut  io.Writer
	tagID   int
	created map[string]int
	failed  int
}

func (a *applier) fail(what string, err error) {
	a.failed++
	_, _ = fmt.Fprintf(a.errOut, "netbox-cable-sync: %s: %v\n", what, err)
}

func (a *applier) skip(l resolvedLink, why string) {
	a.failed++
	_, _ = fmt.Fprintf(a.errOut, "netbox-cable-sync: skipping cable %s: %s\n", l, why)
}

func (a *applier) createCable(l resolvedLink) {
	var ids [2]int
	for n, e := range []resolvedEnd{l.a, l.b} {
		ids[n] = a.created[e.key()]
		if e.iface != nil {
			ids[n] = e.iface.ID
		}
	}
	switch {
	case a.tagID == 0:
		a.skip(l, "the tag is not available")
		return
	case ids[0] == 0 || ids[1] == 0:
		a.skip(l, "an interface was not created")
		return
	}
	body := cableBody{
		A:      []termination{{ObjectType: "dcim.interface", ObjectID: ids[0]}},
		B:      []termination{{ObjectType: "dcim.interface", ObjectID: ids[1]}},
		Status: "connected",
		Tags:   []nbRef{{ID: a.tagID}},
	}
	if err := a.c.do(a.ctx, http.MethodPost, "/api/dcim/cables/", body, nil, http.StatusCreated); err != nil {
		a.fail("creating cable "+l.String(), err)
		return
	}
	_, _ = fmt.Fprintf(a.out, "created cable %s\n", l)
}

func (a *applier) deleteCable(d cableDelete) bool {
	if err := a.c.do(a.ctx, http.MethodDelete, "/api/dcim/cables/"+strconv.Itoa(d.id)+"/", nil, nil, http.StatusNoContent); err != nil {
		a.fail("deleting cable "+d.desc, err)
		return false
	}
	_, _ = fmt.Fprintf(a.out, "deleted cable %s\n", d.desc)
	return true
}

// apply writes the plan in an order a failure cannot make worse: the tag and the interfaces, the
// cables that replace nothing, the stale cables, then each replacement (its delete, then its
// cable). It returns the number of failures.
func apply(ctx context.Context, c *apiClient, x *nbIndex, tag string, p *plan, out, errOut io.Writer) int {
	a := &applier{ctx: ctx, c: c, out: out, errOut: errOut, tagID: x.tagID, created: map[string]int{}}
	if len(p.creates) > 0 && a.tagID == 0 {
		id, err := createTag(ctx, c, tag)
		if err != nil {
			a.fail("creating tag "+tag, err)
		}
		a.tagID = id
	}
	for _, i := range p.interfaces {
		var got nbRef
		body := interfaceBody{Device: i.device.id, Name: i.name, Label: i.label, Type: i.ptype}
		if err := c.do(ctx, http.MethodPost, "/api/dcim/interfaces/", body, &got, http.StatusCreated); err != nil {
			a.fail(fmt.Sprintf("creating interface %s:%s", i.device.name, i.name), err)
			continue
		}
		a.created[nameKey(i.device.id, i.name)] = got.ID
		_, _ = fmt.Fprintf(out, "created interface %s:%s\n", i.device.name, i.name)
	}

	replaced := map[int]bool{}
	for _, l := range p.creates {
		for _, id := range l.replaces {
			replaced[id] = true
		}
	}
	for _, l := range p.creates {
		if len(l.replaces) == 0 {
			a.createCable(l)
		}
	}
	byID := map[int]cableDelete{}
	for _, d := range p.deletes {
		byID[d.id] = d
		if !replaced[d.id] {
			a.deleteCable(d)
		}
	}
	gone := map[int]bool{}
	for _, l := range p.creates {
		if len(l.replaces) == 0 {
			continue
		}
		if a.tagID == 0 {
			a.skip(l, "the tag is not available")
			continue
		}
		ok := true
		for _, id := range l.replaces {
			if !gone[id] && !a.deleteCable(byID[id]) {
				ok = false
				break
			}
			gone[id] = true
		}
		if !ok {
			a.skip(l, "the cable it replaces was not deleted")
			continue
		}
		a.createCable(l)
	}
	return a.failed
}

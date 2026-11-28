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
	Type   string `json:"type"`
}

// apply writes the plan in an order a failure cannot make worse: the tag, the stale cables, the
// interfaces, then the cables. It returns the number of writes that failed; a cable whose interface
// could not be created is skipped and counted.
func apply(ctx context.Context, c *apiClient, tag string, p *plan, out, errOut io.Writer) int {
	failed := 0
	fail := func(what string, err error) {
		failed++
		_, _ = fmt.Fprintf(errOut, "netbox-cable-sync: %s: %v\n", what, err)
	}

	tagID := 0
	if len(p.creates) > 0 {
		var err error
		if tagID, err = ensureTag(ctx, c, tag); err != nil {
			fail("tag "+tag, err)
			return failed + len(p.creates)
		}
	}
	for _, d := range p.deletes {
		if err := c.do(ctx, http.MethodDelete, "/api/dcim/cables/"+strconv.Itoa(d.id)+"/", nil, nil, http.StatusNoContent); err != nil {
			fail("deleting cable "+d.desc, err)
			continue
		}
		_, _ = fmt.Fprintf(out, "deleted cable %s\n", d.desc)
	}
	created := map[string]int{}
	for _, i := range p.interfaces {
		var got nbRef
		body := interfaceBody{Device: i.device.id, Name: i.name, Type: i.ptype}
		if err := c.do(ctx, http.MethodPost, "/api/dcim/interfaces/", body, &got, http.StatusCreated); err != nil {
			fail(fmt.Sprintf("creating interface %s:%s", i.device.name, i.name), err)
			continue
		}
		created[nameKey(i.device.id, i.name)] = got.ID
		_, _ = fmt.Fprintf(out, "created interface %s:%s\n", i.device.name, i.name)
	}
	for _, l := range p.creates {
		ids := [2]int{}
		missing := false
		for n, e := range []resolvedEnd{l.a, l.b} {
			if e.iface != nil {
				ids[n] = e.iface.ID
			} else if ids[n] = created[e.key()]; ids[n] == 0 {
				missing = true
			}
		}
		if missing {
			failed++
			_, _ = fmt.Fprintf(errOut, "netbox-cable-sync: skipping cable %s: an interface was not created\n", l)
			continue
		}
		body := cableBody{
			A:      []termination{{ObjectType: "dcim.interface", ObjectID: ids[0]}},
			B:      []termination{{ObjectType: "dcim.interface", ObjectID: ids[1]}},
			Status: "connected",
			Tags:   []nbRef{{ID: tagID}},
		}
		if err := c.do(ctx, http.MethodPost, "/api/dcim/cables/", body, nil, http.StatusCreated); err != nil {
			fail("creating cable "+l.String(), err)
			continue
		}
		_, _ = fmt.Fprintf(out, "created cable %s\n", l)
	}
	return failed
}

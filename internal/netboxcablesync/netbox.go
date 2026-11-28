package netboxcablesync

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type nbInterface struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Device     nbDevice `json:"device"`
	Type       nbType   `json:"type"`
	MACAddress string   `json:"mac_address"`
	PrimaryMAC *nbMAC   `json:"primary_mac_address"`
	MACs       []nbMAC  `json:"mac_addresses"`
	Cable      *nbRef   `json:"cable"`
}

type nbDevice struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type nbType struct {
	Value string `json:"value"`
}

type nbMAC struct {
	MACAddress string `json:"mac_address"`
}

type nbRef struct {
	ID int `json:"id"`
}

type nbCable struct {
	ID   int `json:"id"`
	Tags []struct {
		Slug string `json:"slug"`
	} `json:"tags"`
	A []nbTermination `json:"a_terminations"`
	B []nbTermination `json:"b_terminations"`
}

type nbTermination struct {
	ObjectType string `json:"object_type"`
	ObjectID   int    `json:"object_id"`
}

type nbTag struct {
	ID   int    `json:"id"`
	Slug string `json:"slug"`
}

type deviceRef struct {
	id   int
	name string
}

// nbIndex is NetBox as the planner reads it.
type nbIndex struct {
	interfaces []*nbInterface
	byID       map[int]*nbInterface
	byName     map[string]*nbInterface
	byMAC      map[string][]*nbInterface
	cables     map[int]*nbCable
}

func nameKey(deviceID int, name string) string {
	return fmt.Sprintf("%d/%s", deviceID, name)
}

// unmanaged are the interface types NetBox refuses a cable on.
var unmanaged = map[string]bool{"virtual": true, "bridge": true, "lag": true}

func (i *nbInterface) macs() []string {
	var out []string
	add := func(raw string) {
		if m := normMAC(raw); m != "" {
			out = append(out, m)
		}
	}
	if i.PrimaryMAC != nil {
		add(i.PrimaryMAC.MACAddress)
	}
	for _, m := range i.MACs {
		add(m.MACAddress)
	}
	add(i.MACAddress)
	return out
}

func newIndex(interfaces []nbInterface, cables []nbCable) *nbIndex {
	idx := &nbIndex{
		byID:   map[int]*nbInterface{},
		byName: map[string]*nbInterface{},
		byMAC:  map[string][]*nbInterface{},
		cables: map[int]*nbCable{},
	}
	for n := range interfaces {
		i := &interfaces[n]
		idx.interfaces = append(idx.interfaces, i)
		idx.byID[i.ID] = i
		idx.byName[nameKey(i.Device.ID, i.Name)] = i
		seen := map[string]bool{}
		for _, m := range i.macs() {
			if !seen[m] {
				seen[m] = true
				idx.byMAC[m] = append(idx.byMAC[m], i)
			}
		}
	}
	for n := range cables {
		idx.cables[cables[n].ID] = &cables[n]
	}
	return idx
}

// device finds the one NetBox device owning an interface with any of macs.
func (x *nbIndex) device(macs []string) (deviceRef, string) {
	found := map[int]deviceRef{}
	for _, m := range macs {
		for _, i := range x.byMAC[m] {
			found[i.Device.ID] = deviceRef{id: i.Device.ID, name: i.Device.Name}
		}
	}
	switch len(found) {
	case 0:
		return deviceRef{}, "no NetBox device has an interface with MAC " + strings.Join(macs, " or ")
	case 1:
		for _, d := range found {
			return d, ""
		}
	}
	return deviceRef{}, "several NetBox devices have an interface with MAC " + strings.Join(macs, " or ")
}

// clientInterface finds the one NetBox interface carrying mac that a cable can be attached to.
func (x *nbIndex) clientInterface(mac string) (*nbInterface, string) {
	var found []*nbInterface
	for _, i := range x.byMAC[mac] {
		if !unmanaged[i.Type.Value] {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 0:
		if len(x.byMAC[mac]) > 0 {
			return nil, "only virtual, bridge or LAG NetBox interfaces carry MAC " + mac
		}
		return nil, "no NetBox interface carries MAC " + mac
	case 1:
		return found[0], ""
	}
	return nil, "several NetBox interfaces carry MAC " + mac
}

func loadNetbox(ctx context.Context, c *apiClient, tag string) (*nbIndex, error) {
	interfaces, err := listAll[nbInterface](ctx, c, "/api/dcim/interfaces/")
	if err != nil {
		return nil, err
	}
	cables, err := listAll[nbCable](ctx, c, "/api/dcim/cables/?tag="+url.QueryEscape(tag))
	if err != nil {
		return nil, err
	}
	return newIndex(interfaces, cables), nil
}

func ensureTag(ctx context.Context, c *apiClient, slug string) (int, error) {
	tags, err := listAll[nbTag](ctx, c, "/api/extras/tags/?slug="+url.QueryEscape(slug))
	if err != nil {
		return 0, err
	}
	for _, t := range tags {
		if t.Slug == slug {
			return t.ID, nil
		}
	}
	var created nbTag
	body := map[string]string{"name": slug, "slug": slug}
	if err = c.do(ctx, http.MethodPost, "/api/extras/tags/", body, &created, http.StatusCreated); err != nil {
		return 0, err
	}
	return created.ID, nil
}

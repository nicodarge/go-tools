package netboxcablesync

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type nbInterface struct {
	ID            int      `json:"id"`
	Name          string   `json:"name"`
	Device        nbDevice `json:"device"`
	Type          nbType   `json:"type"`
	MarkConnected bool     `json:"mark_connected"`
	MACAddress    string   `json:"mac_address"`
	PrimaryMAC    *nbMAC   `json:"primary_mac_address"`
	MACs          []nbMAC  `json:"mac_addresses"`
	Cable         *nbRef   `json:"cable"`
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

// nbIndex is NetBox as the planner reads it. tagID is 0 while the tag does not exist.
type nbIndex struct {
	byID     map[int]*nbInterface
	byName   map[string]*nbInterface
	byMAC    map[string][]*nbInterface
	byDevice map[int][]*nbInterface
	cables   map[int]*nbCable
	tagID    int
}

func nameKey(deviceID int, name string) string {
	return fmt.Sprintf("%d/%s", deviceID, name)
}

// connectable reports whether a cable can be attached to the interface: not virtual, bridge, LAG or
// wireless, and not marked connected.
func (i *nbInterface) connectable() bool {
	switch i.Type.Value {
	case "virtual", "bridge", "lag":
		return false
	}
	return !strings.HasPrefix(i.Type.Value, "ieee802.11") && !i.MarkConnected
}

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

func newIndex(interfaces []nbInterface, cables []nbCable, tagID int) *nbIndex {
	idx := &nbIndex{
		byID:     map[int]*nbInterface{},
		byName:   map[string]*nbInterface{},
		byMAC:    map[string][]*nbInterface{},
		byDevice: map[int][]*nbInterface{},
		cables:   map[int]*nbCable{},
		tagID:    tagID,
	}
	for n := range interfaces {
		i := &interfaces[n]
		i.Name = printable(i.Name, maxText)
		i.Device.Name = printable(i.Device.Name, maxText)
		idx.byID[i.ID] = i
		idx.byName[nameKey(i.Device.ID, i.Name)] = i
		idx.byDevice[i.Device.ID] = append(idx.byDevice[i.Device.ID], i)
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
		if i.connectable() {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 0:
		if len(x.byMAC[mac]) > 0 {
			return nil, "only interfaces no cable can be attached to (virtual, bridge, LAG, wireless or marked connected) carry MAC " + mac
		}
		return nil, "no NetBox interface carries MAC " + mac
	case 1:
		return found[0], ""
	}
	return nil, "several NetBox interfaces carry MAC " + mac
}

// loadNetbox reads the interfaces and, when the tag exists, the cables carrying it: NetBox answers
// 400 to a cable query for a tag that does not exist.
func loadNetbox(ctx context.Context, c *apiClient, tag string) (*nbIndex, error) {
	interfaces, err := listAll[nbInterface](ctx, c, "/api/dcim/interfaces/")
	if err != nil {
		return nil, err
	}
	tags, err := listAll[nbTag](ctx, c, "/api/extras/tags/?slug="+url.QueryEscape(tag))
	if err != nil {
		return nil, err
	}
	tagID := 0
	for _, t := range tags {
		if t.Slug == tag {
			tagID = t.ID
		}
	}
	var cables []nbCable
	if tagID != 0 {
		if cables, err = listAll[nbCable](ctx, c, "/api/dcim/cables/?tag="+url.QueryEscape(tag)); err != nil {
			return nil, err
		}
	}
	return newIndex(interfaces, cables, tagID), nil
}

func createTag(ctx context.Context, c *apiClient, slug string) (int, error) {
	var created nbTag
	body := map[string]string{"name": slug, "slug": slug}
	if err := c.do(ctx, http.MethodPost, "/api/extras/tags/", body, &created, http.StatusCreated); err != nil {
		return 0, err
	}
	return created.ID, nil
}

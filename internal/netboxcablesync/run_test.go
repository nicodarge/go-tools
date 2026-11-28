package netboxcablesync

import (
	"slices"
	"strings"
	"testing"
)

const (
	lineCableClient = "create cable sw1:Port 1 <-> pi:eth0 (client)"
	lineCableLLDP   = "create cable sw1:SFP+ 1 <-> gw:Port 5 (lldp)"
)

func (f *fakeNetbox) deletes() []string {
	var out []string
	for _, w := range f.writes {
		if strings.HasPrefix(w, "DELETE") {
			out = append(out, w)
		}
	}
	return out
}

func lastIndex(values []string, v string) int {
	for i := len(values) - 1; i >= 0; i-- {
		if values[i] == v {
			return i
		}
	}
	return -1
}

func TestFirstRunDryRunWithoutTagPrintsPlanAndWritesNothing(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	lines := r.lines()
	for _, want := range []string{
		"create interface sw1:Port 1 (1000base-t)",
		"create interface sw1:SFP+ 1 (10gbase-x-sfpp)",
		lineCableClient,
		lineCableLLDP,
		"summary: 0 cables in place, 2 to create, 0 to delete, 2 interfaces to create, 0 stale, 0 kept, 0 conflicts, 0 unmatched",
	} {
		if !lines[want] {
			t.Errorf("missing line %q in:\n%s", want, r.out)
		}
	}
	if strings.Count(r.out, "create cable") != 2 {
		t.Errorf("the LLDP link seen from both sides is not counted once:\n%s", r.out)
	}
	if len(n.writes) != 0 || len(n.tags) != 0 {
		t.Errorf("a dry run wrote: %v, tags %v", n.writes, n.tags)
	}
	r.noSecrets(t)
}

func TestFirstRunApplyCreatesTheTagAndIsIdempotent(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u, "--apply")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	for _, want := range []string{"created interface sw1:Port 1", "created interface sw1:SFP+ 1", "created cable sw1:Port 1 <-> pi:eth0", "created cable sw1:SFP+ 1 <-> gw:Port 5"} {
		if !r.lines()[want] {
			t.Errorf("missing line %q in:\n%s", want, r.out)
		}
	}
	if len(n.tags) != 1 || n.tags[0]["slug"] != "unifi-sync" {
		t.Errorf("tag not created: %v", n.tags)
	}
	if len(n.cables) != 2 {
		t.Fatalf("want 2 cables, got %d", len(n.cables))
	}
	for _, c := range n.cables {
		if !hasTag(c, "unifi-sync") {
			t.Errorf("cable without the tag: %v", c)
		}
	}
	if i := n.ifaceByName("sw1", "SFP+ 1"); i == nil || i["type"].(map[string]any)["value"] != "10gbase-x-sfpp" {
		t.Errorf("interface SFP+ 1 not created with its media type: %v", i)
	}
	r.noSecrets(t)

	before := len(n.writes)
	again := n.run(t, u, "--apply")
	if again.code != 0 || len(n.writes) != before {
		t.Errorf("second run wrote: exit %d, writes %v", again.code, n.writes[before:])
	}
	if !again.lines()["summary: 2 cables in place, 0 to create, 0 to delete, 0 interfaces to create, 0 stale, 0 kept, 0 conflicts, 0 unmatched"] {
		t.Errorf("second run is not a no-op:\n%s", again.out)
	}
}

func TestManualCableIsAConflictAndNeverTouched(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	n.addCable(500, 4, 3, "")
	r := n.run(t, u, "--apply", "--prune")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !strings.Contains(r.out, "conflict sw1:Port 1 <-> pi:eth0 (client): pi:eth0 already has cable #500, which this tool does not manage") {
		t.Errorf("no conflict reported:\n%s", r.out)
	}
	if len(n.cables) != 1 || len(n.writes) != 0 {
		t.Errorf("nothing may change: cables %v, writes %v", n.cables, n.writes)
	}
}

func staleSetup(t *testing.T) (*fakeUnifi, *fakeNetbox) {
	t.Helper()
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, append(baseIfaces(),
		iface(5, 11, "gw", "Port 6", "1000base-t", ""), iface(6, 10, "sw1", "Port 2", "1000base-t", ""),
		iface(7, 13, "old", "eth1", "1000base-t", ""), iface(8, 13, "old", "eth2", "1000base-t", "")))
	n.addCable(600, 5, 6, "unifi-sync")
	n.addCable(601, 7, 8, "")
	return u, n
}

func TestStaleTaggedCableNeedsPrune(t *testing.T) {
	t.Run("without prune", func(t *testing.T) {
		u, n := staleSetup(t)
		r := n.run(t, u, "--apply")
		if r.code != 0 {
			t.Fatalf("exit %d: %s", r.code, r.errOut)
		}
		if !strings.Contains(r.out, "stale cable #600 gw:Port 6 <-> sw1:Port 2: link no longer in UniFi; --prune would delete it") {
			t.Errorf("stale cable not reported:\n%s", r.out)
		}
		if d := n.deletes(); len(d) != 0 {
			t.Errorf("deleted without --prune: %v", d)
		}
	})
	t.Run("with prune", func(t *testing.T) {
		u, n := staleSetup(t)
		r := n.run(t, u, "--apply", "--prune")
		if r.code != 0 {
			t.Fatalf("exit %d: %s", r.code, r.errOut)
		}
		if !r.lines()["deleted cable #600 gw:Port 6 <-> sw1:Port 2"] {
			t.Errorf("tagged cable not deleted:\n%s", r.out)
		}
		if d := n.deletes(); len(d) != 1 || d[0] != "DELETE /api/dcim/cables/600/" {
			t.Errorf("only the tagged cable may be deleted: %v", d)
		}
	})
}

func replaceSetup(t *testing.T) (*fakeUnifi, *fakeNetbox) {
	t.Helper()
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, append(baseIfaces(), iface(5, 10, "sw1", "Port 1", "1000base-t", ""), iface(6, 13, "old", "eth0", "1000base-t", "")))
	n.addCable(600, 4, 6, "unifi-sync")
	return u, n
}

func TestStaleTaggedCableOnADesiredPortIsReplacedOnlyWithPrune(t *testing.T) {
	t.Run("without prune", func(t *testing.T) {
		u, n := replaceSetup(t)
		r := n.run(t, u, "--apply")
		if !strings.Contains(r.out, "pi:eth0 has stale cable #600 (pi:eth0 <-> old:eth0); --prune would replace it") {
			t.Errorf("no conflict on the stale cable:\n%s", r.out)
		}
		if len(n.cables) != 2 {
			t.Errorf("cables changed: %v", n.cables)
		}
	})
	t.Run("with prune", func(t *testing.T) {
		u, n := replaceSetup(t)
		r := n.run(t, u, "--apply", "--prune")
		if r.code != 0 || !r.lines()["deleted cable #600 pi:eth0 <-> old:eth0"] || !r.lines()["created cable sw1:Port 1 <-> pi:eth0"] {
			t.Fatalf("not replaced: exit %d\n%s%s", r.code, r.out, r.errOut)
		}
		first := slices.Index(n.writes, "POST /api/dcim/cables/")
		del := slices.Index(n.writes, "DELETE /api/dcim/cables/600/")
		last := lastIndex(n.writes, "POST /api/dcim/cables/")
		if first < 0 || first > del || del > last {
			t.Errorf("the cable that replaces nothing must come first, then the delete, then the replacement: %v", n.writes)
		}
	})
}

func TestDeleteFailureSkipsOnlyTheReplacement(t *testing.T) {
	u, n := replaceSetup(t)
	n.failDelete = true
	r := n.run(t, u, "--apply", "--prune")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", r.code, r.out, r.errOut)
	}
	if len(n.cables) != 2 || n.ifaceByName("sw1", "SFP+ 1") == nil {
		t.Errorf("the independent cable must still be created, the stale one kept: %v", n.cables)
	}
	for _, want := range []string{"deleting cable #600 pi:eth0 <-> old:eth0", "skipping cable sw1:Port 1 <-> pi:eth0: the cable it replaces was not deleted", "2 writes failed"} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
		}
	}
}

func TestTagCreationFailureSkipsEveryCable(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	n.failTags = true
	r := n.run(t, u, "--apply")
	if r.code != 1 || len(n.cables) != 0 {
		t.Fatalf("exit %d, cables %v\n%s", r.code, n.cables, r.errOut)
	}
	for _, want := range []string{"creating tag unifi-sync", "the tag is not available", "3 writes failed"} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
		}
	}
}

func TestInterfaceCreationFailureSkipsItsCableOnly(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	n.failIface = "Port 1"
	r := n.run(t, u, "--apply")
	if r.code != 1 || len(n.cables) != 1 {
		t.Fatalf("exit %d, cables %v\n%s", r.code, n.cables, r.errOut)
	}
	for _, want := range []string{"creating interface sw1:Port 1", "skipping cable sw1:Port 1 <-> pi:eth0: an interface was not created", "2 writes failed"} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
		}
	}
	if !r.lines()["created cable sw1:SFP+ 1 <-> gw:Port 5"] {
		t.Errorf("the other cable was not attempted:\n%s", r.out)
	}
}

func TestCableWriteFailureExitsOne(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	n.failCables = true
	r := n.run(t, u, "--apply")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", r.code, r.out, r.errOut)
	}
	if !strings.Contains(r.errOut, "HTTP 500: boom") || !strings.Contains(r.errOut, "2 writes failed") {
		t.Errorf("failure not reported: %s", r.errOut)
	}
	r.noSecrets(t)
}

func TestCustomTagOwnsItsOwnCablesOnly(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, append(baseIfaces(), iface(5, 13, "old", "eth0", "1000base-t", ""), iface(6, 13, "old", "eth1", "1000base-t", "")))
	n.addCable(600, 5, 6, "unifi-sync")
	r := n.run(t, u, "--apply", "--prune", "--tag", "lab-sync")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if strings.Contains(r.out, "#600") {
		t.Errorf("a cable of another tag was considered:\n%s", r.out)
	}
}

func TestTwoClientsBehindOnePortAreAmbiguous(t *testing.T) {
	clients := append(baseClients(), wired(macNAS, "nas", macSW1, 1))
	ifaces := append(baseIfaces(), iface(7, 14, "nas", "eth0", "1000base-t", macNAS))
	u := newFakeUnifi(t, baseDevices(), clients)
	n := newFakeNetbox(t, ifaces)
	r := n.run(t, u, "--apply")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if strings.Count(r.out, "conflict ") != 2 || !strings.Contains(r.out, "sw1:Port 1 carries several links") {
		t.Errorf("ambiguity not reported:\n%s", r.out)
	}
	if strings.Contains(r.out, "create cable sw1:Port 1") || n.ifaceByName("sw1", "Port 1") != nil {
		t.Errorf("a cable or interface was planned for the ambiguous port:\n%s", r.out)
	}
}

func TestAHostSeenByLLDPAndAsAClientIsOneCable(t *testing.T) {
	devices := withLLDP(baseDevices(), 0, lldp(macGW, 25, "eth25", "Port 5"), lldp(macPi, 1, "eth1", "eth0"))
	u := newFakeUnifi(t, devices, baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !r.lines()["create cable sw1:Port 1 <-> pi:eth0 (lldp)"] || strings.Count(r.out, "create cable sw1:Port 1") != 1 || strings.Contains(r.out, "conflict ") {
		t.Errorf("want one LLDP cable and no conflict:\n%s", r.out)
	}
}

func TestOnlyUniFiPortsAreCreated(t *testing.T) {
	devices := withLLDP(baseDevices(), 0, lldp(macGW, 25, "eth25", "Port 5"), lldp(macPi, 2, "eth2", "eth9"))
	u := newFakeUnifi(t, devices, baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u, "--apply")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !strings.Contains(r.out, "(lldp): no NetBox interface eth9 on pi") {
		t.Errorf("the missing neighbour port is not reported:\n%s", r.out)
	}
	if n.ifaceByName("pi", "eth9") != nil || n.ifaceByName("sw1", "Port 2") != nil {
		t.Error("an interface was created for an unmatched link")
	}
}

func TestUnmatchedEndsAreReportedNotCreated(t *testing.T) {
	clients := append(baseClients(),
		wired("a0:bb:cc:00:00:55", "ghost", macSW1, 2),
		wired("a0:bb:cc:00:00:56", "vmhost", macSW1, 2),
		wired("a0:bb:cc:00:00:57", "laptop", macSW1, 2),
		wired("a0:bb:cc:00:00:58", "appliance", macSW1, 2))
	marked := iface(10, 16, "appliance", "eth0", "1000base-t", "a0:bb:cc:00:00:58")
	marked["mark_connected"] = true
	ifaces := append(baseIfaces(),
		iface(8, 15, "bridgehost", "br0", "bridge", "a0:bb:cc:00:00:56"),
		iface(9, 17, "laptop", "wlan0", "ieee802.11ax", "a0:bb:cc:00:00:57"),
		marked)
	u := newFakeUnifi(t, baseDevices(), clients)
	n := newFakeNetbox(t, ifaces)
	r := n.run(t, u, "--apply")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	const noCable = "only interfaces no cable can be attached to (virtual, bridge, LAG, wireless or marked connected) carry MAC "
	for _, want := range []string{
		"unmatched sw1:Port 2 <-> ghost (client): no NetBox interface carries MAC a0:bb:cc:00:00:55",
		"unmatched sw1:Port 2 <-> vmhost (client): " + noCable + "a0:bb:cc:00:00:56",
		"unmatched sw1:Port 2 <-> laptop (client): " + noCable + "a0:bb:cc:00:00:57",
		"unmatched sw1:Port 2 <-> appliance (client): " + noCable + "a0:bb:cc:00:00:58",
	} {
		if !r.lines()[want] {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if n.ifaceByName("sw1", "Port 2") != nil {
		t.Error("an interface was created for an unmatched link")
	}
}

func TestMACCollisionsInNetboxAreUnmatched(t *testing.T) {
	ifaces := append(baseIfaces(), iface(5, 20, "dup", "eth0", "1000base-t", macPi), iface(6, 21, "sw1b", "mgmt", "virtual", macSW1), iface(7, 22, "blank", "eth0", "1000base-t", ""))
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, ifaces)
	r := n.run(t, u, "--apply")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	for _, want := range []string{"several NetBox interfaces carry MAC " + macPi, "several NetBox devices have an interface with MAC " + macSW1} {
		if !strings.Contains(r.out, want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if strings.Contains(r.out, "create cable sw1") || len(n.writes) != 0 {
		t.Errorf("an ambiguous end was cabled: %v\n%s", n.writes, r.out)
	}
}

func TestLocallyAdministeredClientsAreCountedOnce(t *testing.T) {
	clients := append(baseClients(), wired("52:54:00:00:00:01", "vm1", macSW1, 2), wired("5a:00:00:00:00:02", "vm2", macSW1, 2))
	u := newFakeUnifi(t, baseDevices(), clients)
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !r.lines()["unmatched 2 wired client(s) with a locally administered MAC skipped"] || strings.Count(r.out, "unmatched") != 2 {
		t.Errorf("want one count line:\n%s", r.out)
	}
}

func TestPruneKeepsCablesOnPortsUniFiCannotJudge(t *testing.T) {
	devices := withLLDP(baseDevices(), 0,
		lldp(macGW, 25, "eth25", "00:00:00:00:00:99"),
		lldp("not-a-mac", 2, "eth2", "x"))
	devices = withLLDP(devices, 1)
	clients := []any{wired(macPi, "pi", "00:00:00:00:09:09", 1)}
	ifaces := append(baseIfaces(),
		iface(5, 10, "sw1", "SFP+ 1", "10gbase-x-sfpp", ""), iface(6, 10, "sw1", "Port 3", "1000base-t", ""),
		iface(7, 10, "sw1", "Port 2", "1000base-t", ""), iface(8, 10, "sw1", "Port 4", "1000base-t", ""),
		iface(9, 11, "gw", "Port 6", "1000base-t", ""), iface(10, 10, "sw1", "Port 5", "1000base-t", ""),
		iface(11, 10, "sw1", "Port 6", "1000base-t", ""),
		iface(12, 10, "sw1", "Port 7", "1000base-t", ""), iface(13, 10, "sw1", "Port 8", "1000base-t", ""))
	u := newFakeUnifi(t, devices, clients)
	n := newFakeNetbox(t, ifaces)
	n.addCable(600, 5, 6, "unifi-sync")
	n.addCable(601, 7, 8, "unifi-sync")
	n.addCable(602, 9, 10, "unifi-sync")
	n.addCable(603, 4, 11, "unifi-sync")
	n.addCable(604, 12, 13, "unifi-sync")
	r := n.run(t, u, "--prune")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	for _, want := range []string{
		"kept cable #600 sw1:SFP+ 1 <-> sw1:Port 3: a port of it is on a link or device UniFi does not report reliably",
		"kept cable #601 sw1:Port 2 <-> sw1:Port 4: a port of it is on a link or device UniFi does not report reliably",
		"kept cable #602 gw:Port 6 <-> sw1:Port 5: a port of it is on a link or device UniFi does not report reliably",
		"kept cable #603 pi:eth0 <-> sw1:Port 6: a port of it is on a link or device UniFi does not report reliably",
		"delete cable #604 sw1:Port 7 <-> sw1:Port 8 (link no longer in UniFi)",
	} {
		if !r.lines()[want] {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if strings.Count(r.out, "delete cable") != 1 {
		t.Errorf("only the control cable may be deleted:\n%s", r.out)
	}
}

func TestPruneKeepsACableWhoseClientIsNotSeen(t *testing.T) {
	setup := func(t *testing.T) *fakeNetbox {
		t.Helper()
		n := newFakeNetbox(t, append(baseIfaces(), iface(6, 10, "sw1", "Port 2", "1000base-t", ""), iface(7, 14, "nas", "eth0", "1000base-t", macNAS)))
		n.addCable(600, 6, 7, "unifi-sync")
		return n
	}
	t.Run("offline", func(t *testing.T) {
		u := newFakeUnifi(t, baseDevices(), baseClients())
		r := setup(t).run(t, u, "--prune")
		if !strings.Contains(r.out, "kept cable #600 sw1:Port 2 <-> nas:eth0: an end UniFi does not manage was not seen on any port now") || strings.Contains(r.out, "delete cable") {
			t.Errorf("the cable of an offline client must be kept:\n%s", r.out)
		}
	})
	t.Run("seen on another port", func(t *testing.T) {
		u := newFakeUnifi(t, baseDevices(), append(baseClients(), wired(macNAS, "nas", macGW, 6)))
		r := setup(t).run(t, u, "--prune")
		if !r.lines()["delete cable #600 sw1:Port 2 <-> nas:eth0 (replaced by gw:Port 6 <-> nas:eth0)"] || !r.lines()["create cable gw:Port 6 <-> nas:eth0 (client)"] {
			t.Errorf("the cable of a client seen elsewhere must be replaced:\n%s", r.out)
		}
	})
}

func TestPruneKeepsTheCablesOfADisconnectedDevice(t *testing.T) {
	devices := withField(baseDevices(), 1, "state", 0)
	u := newFakeUnifi(t, devices, baseClients())
	n := newFakeNetbox(t, append(baseIfaces(),
		iface(5, 11, "gw", "Port 6", "1000base-t", ""), iface(6, 10, "sw1", "Port 2", "1000base-t", ""),
		iface(7, 10, "sw1", "Port 3", "1000base-t", ""), iface(8, 10, "sw1", "Port 4", "1000base-t", "")))
	n.addCable(600, 5, 6, "unifi-sync")
	n.addCable(601, 7, 8, "unifi-sync")
	r := n.run(t, u, "--prune")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	for _, want := range []string{
		"kept cable #600 gw:Port 6 <-> sw1:Port 2: a port of it is on a link or device UniFi does not report reliably",
		"delete cable #601 sw1:Port 3 <-> sw1:Port 4 (link no longer in UniFi)",
		"unmatched device gw is not connected (state 0): its ports are kept out of --prune",
	} {
		if !r.lines()[want] {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
}

func TestUnresolvedLinkProtectsItsTaggedCableFromPrune(t *testing.T) {
	clients := append(baseClients(), wired("a0:bb:cc:00:00:55", "ghost", macSW1, 2))
	u := newFakeUnifi(t, baseDevices(), clients)
	n := newFakeNetbox(t, append(baseIfaces(), iface(5, 10, "sw1", "Port 2", "1000base-t", ""), iface(6, 13, "old", "eth0", "1000base-t", "")))
	n.addCable(600, 5, 6, "unifi-sync")
	r := n.run(t, u, "--apply", "--prune")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !strings.Contains(r.out, "kept cable #600 sw1:Port 2 <-> old:eth0") {
		t.Errorf("the cable is not reported as kept:\n%s", r.out)
	}
	if d := n.deletes(); len(d) != 0 {
		t.Errorf("pruned a cable on an unresolved port: %v", d)
	}
}

func TestV2TokenIsSentAsBearerEndToEnd(t *testing.T) {
	const token = "nbt_abc123.secretpart"
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	n.wantAuth = "Bearer " + token
	r := runWith(t, map[string]string{
		"UNIFI_URL": u.srv.URL, "UNIFI_API_KEY": unifiKey,
		"NETBOX_URL": n.srv.URL, "NETBOX_TOKEN": token,
	})
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if len(n.auth) == 0 || slices.ContainsFunc(n.auth, func(a string) bool { return a != "Bearer "+token }) {
		t.Errorf("authorization headers: %q", n.auth)
	}
	if strings.Contains(r.out+r.errOut, token) {
		t.Error("the token is in the output")
	}
}

func TestV1TokenIsSentAsToken(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	if r := n.run(t, u); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if slices.ContainsFunc(n.auth, func(a string) bool { return a != "Token "+netboxSecret }) {
		t.Errorf("authorization headers: %q", n.auth)
	}
	if got := netboxAuthorization("nbt_abc.def"); got != "Bearer nbt_abc.def" {
		t.Errorf("v2 token: %q", got)
	}
}

func TestUnifiSiteAndPathAreConfigurable(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	u.prefix = "/legacy/api/s/lab"
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u, "--unifi-site", "lab", "--unifi-api-path", "/legacy/")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if !slices.Equal(u.paths, []string{"/legacy/api/s/lab/stat/device", "/legacy/api/s/lab/stat/sta"}) {
		t.Errorf("requested %q", u.paths)
	}
}

func TestEmptyPathFlagSelectsAStandaloneController(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	u.prefix = "/api/s/default"
	n := newFakeNetbox(t, baseIfaces())
	if r := n.run(t, u, "--unifi-api-path", ""); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	if r := runWith(t, map[string]string{
		"UNIFI_URL": u.srv.URL, "UNIFI_API_KEY": unifiKey, "UNIFI_API_PATH": "",
		"NETBOX_URL": n.srv.URL, "NETBOX_TOKEN": netboxSecret,
	}); r.code != 2 {
		t.Errorf("an empty variable must select the default path, exit %d", r.code)
	}
}

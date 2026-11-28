package netboxcablesync

import (
	"strings"
	"testing"
)

const (
	lineCableClient = "create cable sw1:Port 1 <-> pi:eth0 (client)"
	lineCableLLDP   = "create cable sw1:SFP+ 1 <-> gw:Port 5 (lldp)"
)

func TestDryRunPrintsPlanAndWritesNothing(t *testing.T) {
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
		"summary: 0 cables in place, 2 to create, 0 to delete, 2 interfaces to create, 0 stale, 0 conflicts, 0 unmatched",
	} {
		if !lines[want] {
			t.Errorf("missing line %q in:\n%s", want, r.out)
		}
	}
	if strings.Count(r.out, "create cable") != 2 {
		t.Errorf("the LLDP link seen from both sides is not counted once:\n%s", r.out)
	}
	if len(n.writes) != 0 {
		t.Errorf("a dry run wrote: %v", n.writes)
	}
	r.noSecrets(t)
}

func TestApplyCreatesTaggedCablesAndIsIdempotent(t *testing.T) {
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
	if !again.lines()["summary: 2 cables in place, 0 to create, 0 to delete, 0 interfaces to create, 0 stale, 0 conflicts, 0 unmatched"] {
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
	for _, w := range n.writes {
		if strings.Contains(w, "/cables/500") || strings.HasPrefix(w, "DELETE") {
			t.Errorf("a manual cable was touched: %v", n.writes)
		}
	}
	if len(n.cables) != 1 || len(n.writes) != 0 {
		t.Errorf("nothing may change: cables %v, writes %v", n.cables, n.writes)
	}
}

func TestStaleTaggedCableNeedsPrune(t *testing.T) {
	setup := func(t *testing.T) (*fakeUnifi, *fakeNetbox) {
		t.Helper()
		u := newFakeUnifi(t, baseDevices(), baseClients())
		n := newFakeNetbox(t, append(baseIfaces(),
			iface(5, 13, "old", "eth0", "1000base-t", "aa:bb:cc:00:00:99"), iface(6, 10, "sw1", "Port 2", "1000base-t", ""),
			iface(7, 13, "old", "eth1", "1000base-t", ""), iface(8, 13, "old", "eth2", "1000base-t", "")))
		n.addTag(77, "unifi-sync")
		n.addCable(600, 5, 6, "unifi-sync")
		n.addCable(601, 7, 8, "")
		return u, n
	}
	t.Run("without prune", func(t *testing.T) {
		u, n := setup(t)
		r := n.run(t, u, "--apply")
		if r.code != 0 {
			t.Fatalf("exit %d: %s", r.code, r.errOut)
		}
		if !strings.Contains(r.out, "stale cable #600 old:eth0 <-> sw1:Port 2: link no longer in UniFi; --prune would delete it") {
			t.Errorf("stale cable not reported:\n%s", r.out)
		}
		for _, w := range n.writes {
			if strings.HasPrefix(w, "DELETE") {
				t.Errorf("deleted without --prune: %v", n.writes)
			}
		}
	})
	t.Run("with prune", func(t *testing.T) {
		u, n := setup(t)
		r := n.run(t, u, "--apply", "--prune")
		if r.code != 0 {
			t.Fatalf("exit %d: %s", r.code, r.errOut)
		}
		if !r.lines()["deleted cable #600 old:eth0 <-> sw1:Port 2"] {
			t.Errorf("tagged cable not deleted:\n%s", r.out)
		}
		var deletes []string
		for _, w := range n.writes {
			if strings.HasPrefix(w, "DELETE") {
				deletes = append(deletes, w)
			}
		}
		if len(deletes) != 1 || deletes[0] != "DELETE /api/dcim/cables/600/" {
			t.Errorf("only the tagged cable may be deleted: %v", deletes)
		}
	})
}

func TestStaleTaggedCableOnADesiredPortIsReplacedOnlyWithPrune(t *testing.T) {
	setup := func(t *testing.T) (*fakeUnifi, *fakeNetbox) {
		t.Helper()
		u := newFakeUnifi(t, baseDevices(), baseClients())
		n := newFakeNetbox(t, append(baseIfaces(), iface(5, 10, "sw1", "Port 1", "1000base-t", ""), iface(6, 13, "old", "eth0", "1000base-t", "")))
		n.addTag(77, "unifi-sync")
		n.addCable(600, 4, 6, "unifi-sync")
		return u, n
	}
	t.Run("without prune", func(t *testing.T) {
		u, n := setup(t)
		r := n.run(t, u, "--apply")
		if !strings.Contains(r.out, "pi:eth0 has stale cable #600 (pi:eth0 <-> old:eth0); --prune would replace it") {
			t.Errorf("no conflict on the stale cable:\n%s", r.out)
		}
		if len(n.cables) != 2 {
			t.Errorf("cables changed: %v", n.cables)
		}
	})
	t.Run("with prune", func(t *testing.T) {
		u, n := setup(t)
		r := n.run(t, u, "--apply", "--prune")
		if r.code != 0 || !r.lines()["deleted cable #600 pi:eth0 <-> old:eth0"] || !r.lines()["created cable sw1:Port 1 <-> pi:eth0"] {
			t.Errorf("not replaced: exit %d\n%s%s", r.code, r.out, r.errOut)
		}
	})
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

func TestUnmatchedEndsAreReportedNotCreated(t *testing.T) {
	clients := append(baseClients(), wired("aa:bb:cc:00:00:55", "ghost", macSW1, 2), wired("aa:bb:cc:00:00:56", "vmhost", macSW1, 2))
	ifaces := append(baseIfaces(), iface(8, 15, "bridgehost", "br0", "bridge", "aa:bb:cc:00:00:56"))
	u := newFakeUnifi(t, baseDevices(), clients)
	n := newFakeNetbox(t, ifaces)
	r := n.run(t, u, "--apply")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	for _, want := range []string{
		"unmatched sw1:Port 2 <-> ghost (client): no NetBox interface carries MAC aa:bb:cc:00:00:55",
		"unmatched sw1:Port 2 <-> vmhost (client): only virtual, bridge or LAG NetBox interfaces carry MAC aa:bb:cc:00:00:56",
	} {
		if !r.lines()[want] {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
	if n.ifaceByName("sw1", "Port 2") != nil {
		t.Error("an interface was created for an unmatched link")
	}
}

func TestUnresolvedLinkProtectsItsTaggedCableFromPrune(t *testing.T) {
	clients := append(baseClients(), wired("aa:bb:cc:00:00:55", "ghost", macSW1, 2))
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
	for _, w := range n.writes {
		if strings.HasPrefix(w, "DELETE") {
			t.Errorf("pruned a cable on an unresolved port: %v", n.writes)
		}
	}
}

func TestFailedWriteExitsOne(t *testing.T) {
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

func TestNetboxV2TokenIsABearerToken(t *testing.T) {
	if got := netboxAuthorization("nbt_abc.def"); got != "Bearer nbt_abc.def" {
		t.Errorf("v2 token: %q", got)
	}
	if got := netboxAuthorization("0123abcd"); got != "Token 0123abcd" {
		t.Errorf("v1 token: %q", got)
	}
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	if r := n.run(t, u); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.errOut)
	}
	for _, a := range n.auth {
		if a != "Token "+netboxSecret {
			t.Errorf("authorization %q", a)
		}
	}
}

func TestUnifiPathAndSiteAreConfigurable(t *testing.T) {
	u := newFakeUnifi(t, baseDevices(), baseClients())
	n := newFakeNetbox(t, baseIfaces())
	r := n.run(t, u, "--unifi-site", "lab", "--unifi-api-path", "/legacy/")
	if r.code != 2 {
		t.Fatalf("exit %d, want 2", r.code)
	}
	if got := u.paths[0]; got != "/legacy/api/s/lab/stat/device" {
		t.Errorf("requested %q", got)
	}
}

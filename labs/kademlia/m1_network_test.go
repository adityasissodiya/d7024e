package kademlia

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"strconv"
	"testing"
	"time"
)

// --------- test helpers ---------

func randIDHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, IDLength)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

func newNode(t *testing.T) (*Kademlia, Contact) {
	t.Helper()
	ip := "127.0.0.1"
	port := freeUDPPort(t)
	idHex := randIDHex(t)

	idBytes, _ := hex.DecodeString(idHex)
	var id KademliaID
	copy(id[:], idBytes)
	me := NewContact(&id, net.JoinHostPort(ip, itoa(port)))

	k, err := NewKademlia(me, ip, port)
	if err != nil {
		t.Fatalf("NewKademlia: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k, me
}

func itoa(n int) string { return strconv.Itoa(n) } // cheap, safe ints->string for ports

func waitUntil(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func hasContactWithAddress(k *Kademlia, addr string) bool {
	// Pull a wide set of contacts and scan for the address.
	// Use all-zero target to iterate all buckets (implementation collects across buckets anyway).
	var zero KademliaID
	contacts := k.routingTable.FindClosestContacts(&zero, 100000)
	for _, c := range contacts {
		if c.Address == addr {
			return true
		}
	}
	return false
}

func getAllAddresses(k *Kademlia) map[string]struct{} {
	var zero KademliaID
	contacts := k.routingTable.FindClosestContacts(&zero, 100000)
	m := make(map[string]struct{}, len(contacts))
	for _, c := range contacts {
		m[c.Address] = struct{}{}
	}
	return m
}

// --------- tests ---------

// Test that PING/PONG works and both sides learn each other.
func TestPingAddsBothSides(t *testing.T) {
	a, aMe := newNode(t)
	b, bMe := newNode(t)

	// A -> B ping
	a.network.SendPingMessage(&bMe)

	ok := waitUntil(t, 2*time.Second, func() bool {
		return hasContactWithAddress(a, bMe.Address) && hasContactWithAddress(b, aMe.Address)
	})
	if !ok {
		t.Fatalf("PING failed to populate routing tables: A hasB=%v, B hasA=%v",
			hasContactWithAddress(a, bMe.Address), hasContactWithAddress(b, aMe.Address))
	}
}

// Test Join(): PING + iterative lookup on self ID.
func TestJoinPopulatesRoutingTables(t *testing.T) {
	a, aMe := newNode(t)
	b, bMe := newNode(t)

	if err := a.Join(&bMe); err != nil {
		t.Fatalf("Join: %v", err)
	}

	ok := waitUntil(t, 2*time.Second, func() bool {
		return hasContactWithAddress(a, bMe.Address) && hasContactWithAddress(b, aMe.Address)
	})
	if !ok {
		t.Fatalf("Join did not result in mutual visibility")
	}
}

// Build a small multi-node network and verify A can lookup B via FIND_NODE.
func TestLookupFindsTargetInSmallNetwork(t *testing.T) {
	// Create 8 nodes and chain-join them through the first node as bootstrap.
	nodes := make([]*Kademlia, 0, 8)
	contacts := make([]Contact, 0, 8)
	for i := 0; i < 8; i++ {
		k, me := newNode(t)
		nodes = append(nodes, k)
		contacts = append(contacts, me)
	}
	bootstrap := contacts[0]
	for i := 1; i < len(nodes); i++ {
		if err := nodes[i].Join(&bootstrap); err != nil {
			t.Fatalf("Join node %d: %v", i, err)
		}
	}

	target := contacts[len(contacts)-1] // last node
	origin := nodes[1]

	origin.LookupContact(&target)

	ok := waitUntil(t, 3*time.Second, func() bool {
		return hasContactWithAddress(origin, target.Address)
	})
	if !ok {
		t.Fatalf("LookupContact failed to discover target %s in origin's routing table", target.Address)
	}
}

// Verify FIND_NODE path adds responder and returned contacts to routing table.
func TestFindNodePopulatesDiscoveredContacts(t *testing.T) {
	// Three nodes: A <-> B, C <-> B; A should learn C through querying B.
	a, aMe := newNode(t)
	b, bMe := newNode(t)
	c, cMe := newNode(t)

	// Seed some edges
	a.network.SendPingMessage(&bMe)
	c.network.SendPingMessage(&bMe)

	ok := waitUntil(t, 2*time.Second, func() bool {
		return hasContactWithAddress(a, bMe.Address) && // A learned B
			hasContactWithAddress(c, bMe.Address) && // C learned B
			hasContactWithAddress(b, aMe.Address) && // B learned A (uses b)
			hasContactWithAddress(b, cMe.Address) // B learned C (uses b)
	})
	if !ok {
		t.Fatalf("Initial pings failed to populate via B")
	}

	// A looks up C (through B)
	a.LookupContact(&cMe)

	ok = waitUntil(t, 2*time.Second, func() bool {
		return hasContactWithAddress(a, cMe.Address)
	})
	if !ok {
		// Print some diag
		addrSet := getAllAddresses(a)
		t.Fatalf("A did not learn C via FIND_NODE; A has %d peers; hasC=%v",
			len(addrSet), hasContactWithAddress(a, cMe.Address))
	}
	_ = aMe // silence unused if assert fails earlier
}

// Unresponsive peer: PING should time out and NOT add to routing table.
func TestUnresponsivePeerDoesNotGetAdded(t *testing.T) {
	a, _ := newNode(t)

	// Reserve a free port and close immediately to make it unresponsive.
	unusedPort := freeUDPPort(t)
	bIDHex := randIDHex(t)
	bIDBytes, _ := hex.DecodeString(bIDHex)
	var bID KademliaID
	copy(bID[:], bIDBytes)
	dead := NewContact(&bID, net.JoinHostPort("127.0.0.1", itoa(unusedPort)))

	start := time.Now()
	a.network.SendPingMessage(&dead)
	elapsed := time.Since(start)
	if hasContactWithAddress(a, dead.Address) {
		t.Fatalf("Unresponsive peer should not be added to routing table")
	}
	// Sanity: should not hang forever. (generous bound)
	if elapsed > 2*time.Second {
		t.Fatalf("Ping timeout took too long: %v", elapsed)
	}
}

// Isolated node: LookupContact must not panic or hang when there are no candidates.
func TestLookupOnIsolatedNodeNoPanic(t *testing.T) {
	a, _ := newNode(t)

	// random target
	bIDHex := randIDHex(t)
	bIDBytes, _ := hex.DecodeString(bIDHex)
	var bID KademliaID
	copy(bID[:], bIDBytes)
	target := NewContact(&bID, "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.LookupContact(&target)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatalf("LookupContact on isolated node hung")
	}
}

// Ensure repeated PINGs don't cause duplicates or other weirdness in buckets.
// (We can't inspect buckets directly; just ensure it doesn't panic and remains stable.)
func TestRepeatedPingIsIdempotentEnough(t *testing.T) {
	a, _ := newNode(t)
	b, bMe := newNode(t)

	for i := 0; i < 5; i++ {
		a.network.SendPingMessage(&bMe)
	}

	ok := waitUntil(t, 2*time.Second, func() bool {
		return hasContactWithAddress(a, bMe.Address) && hasContactWithAddress(b, a.me.Address)
	})
	if !ok {
		t.Fatalf("Repeated PINGs did not converge to mutual visibility")
	}

	// Snapshot size then ping again; size should be stable-ish (cannot exceed global contact count).
	before := len(getAllAddresses(a))
	a.network.SendPingMessage(&bMe)
	time.Sleep(100 * time.Millisecond)
	after := len(getAllAddresses(a))
	if after < 1 || after > before+1 {
		t.Fatalf("Unexpected routing table size delta after repeated ping: before=%d after=%d", before, after)
	}
}

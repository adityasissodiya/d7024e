package kademlia

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"net"
	"sort"
	"strconv"
	"testing"
	"time"
)

//
// ------------- M2 test helpers (prefixed with m2*) -------------
//

// m2RandIDHex returns a random 160-bit hex ID (40 hex chars).
func m2RandIDHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, IDLength)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

// m2FreeUDPPort finds a free UDP port on localhost.
func m2FreeUDPPort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

// m2NewNode spins up a single node bound to 127.0.0.1:<free-port>.
func m2NewNode(t *testing.T) (*Kademlia, Contact) {
	t.Helper()
	ip := "127.0.0.1"
	port := m2FreeUDPPort(t)
	idHex := m2RandIDHex(t)

	idBytes, _ := hex.DecodeString(idHex)
	var id KademliaID
	copy(id[:], idBytes)
	me := NewContact(&id, net.JoinHostPort(ip, strconv.Itoa(port)))

	k, err := NewKademlia(me, ip, port)
	if err != nil {
		t.Fatalf("NewKademlia: %v", err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k, me
}

// m2WaitUntil polls until cond() is true or the timeout elapses.
func m2WaitUntil(t *testing.T, d time.Duration, cond func() bool) bool {
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

// m2Cluster creates n nodes, uses node[0] as bootstrap, and joins the rest.
// Returns the nodes and their contacts. All nodes are kept open via t.Cleanup.
func m2Cluster(t *testing.T, n int) ([]*Kademlia, []Contact) {
	t.Helper()
	if n < 2 {
		t.Fatalf("cluster size must be >= 2")
	}
	nodes := make([]*Kademlia, 0, n)
	contacts := make([]Contact, 0, n)
	for i := 0; i < n; i++ {
		k, me := m2NewNode(t)
		nodes = append(nodes, k)
		contacts = append(contacts, me)
	}
	bootstrap := contacts[0]
	// All others join via bootstrap.
	for i := 1; i < len(nodes); i++ {
		if err := nodes[i].Join(&bootstrap); err != nil {
			t.Fatalf("Join node %d: %v", i, err)
		}
	}
	// Give the UDP gossip a brief moment to settle on localhost.
	time.Sleep(150 * time.Millisecond)
	return nodes, contacts
}

// m2KeyHex computes the SHA-1 key hex (40 chars) for data, matching node logic.
func m2KeyHex(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// m2KClosestPeersToKey returns the K closest *peers* (not including origin)
// to the given key, from the provided contacts slice.
func m2KClosestPeersToKey(originIdx int, contacts []Contact, keyHex string) []Contact {
	b, _ := hex.DecodeString(keyHex)
	var id KademliaID
	copy(id[:], b)

	type pair struct {
		c   Contact
		dst *KademliaID
	}
	ps := make([]pair, 0, len(contacts))
	for i, c := range contacts {
		if i == originIdx {
			continue // exclude origin for pure peer list
		}
		ps = append(ps, pair{c: c, dst: c.ID.CalcDistance(&id)})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].dst.Less(ps[j].dst) })

	// Use bucketSize as K (same as replication factor in Put()).
	k := bucketSize
	if k > len(ps) {
		k = len(ps)
	}
	out := make([]Contact, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, ps[i].c)
	}
	return out
}

// m2QueryHasValue asks "peer" if it has the value.
// It uses reqNode's network to send FIND_VALUE to the peer.
// Returns true if the peer directly returned the value.
func m2QueryHasValue(t *testing.T, reqNode *Kademlia, peer *Contact, keyHex string) bool {
	t.Helper()
	val, _, err := reqNode.network.sendFindValueTo(peer, keyHex, 900*time.Millisecond)
	if err != nil {
		// Timeout or network error: treat as not having the value here.
		return false
	}
	return len(val) > 0
}

// m2KClosestFromOriginView returns the K closest contacts to keyHex
// according to the origin node's current routing table.
// This matches Kademlia's behavior (replicate to the K nodes *found by lookup*).
func m2KClosestFromOriginView(t *testing.T, origin *Kademlia, keyHex string) []Contact {
	t.Helper()
	b, err := hex.DecodeString(keyHex)
	if err != nil || len(b) != IDLength {
		t.Fatalf("bad keyHex: %v", err)
	}
	var id KademliaID
	copy(id[:], b)
	// Pull more than K to be safe, then trim in stable order.
	got := origin.routingTable.FindClosestContacts(&id, 1024)
	if len(got) > bucketSize {
		got = got[:bucketSize]
	}
	// Ensure stable distance order
	sort.SliceStable(got, func(i, j int) bool {
		return got[i].ID.CalcDistance(&id).Less(got[j].ID.CalcDistance(&id))
	})
	return got
}

// m2NodeByAddress finds the node instance with a given address.
func m2NodeByAddress(nodes []*Kademlia, addr string) *Kademlia {
	for _, n := range nodes {
		if n.me.Address == addr {
			return n
		}
	}
	return nil
}

// m2WaitHasLocalValue polls a node's local store for keyHex until timeout.
func m2WaitHasLocalValue(t *testing.T, n *Kademlia, keyHex string, d time.Duration) bool {
	t.Helper()
	return m2WaitUntil(t, d, func() bool {
		_, ok := n.loadLocal(keyHex) // unexported but same package
		return ok
	})
}

//
// ------------- Tests -------------
//

// TestM2_PutAndGet_SucceedsAcrossNetwork
// - Create a small network.
// - Put() a blob from node A.
// - Get() the blob from node B; expect value and a reasonable source.
func TestM2_PutAndGet_SucceedsAcrossNetwork(t *testing.T) {
	nodes, _ := m2Cluster(t, 6)
	a := nodes[1]
	b := nodes[2]

	data := []byte("hello world")
	key, err := a.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Try to get from a different node.
	val, from, err := b.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != string(data) {
		t.Fatalf("Get returned wrong bytes: got %q want %q", string(val), string(data))
	}
	if from == nil || from.Address == "" {
		t.Fatalf("Get returned nil/empty source contact")
	}

	// Sanity: the origin node should also have the value locally.
	if !m2WaitHasLocalValue(t, nodes[1], key, 2*time.Second) {
		t.Fatalf("origin node should store value locally after Put")
	}
}

// TestM2_ReplicatesToKClosestIncludingOrigin
// - Compute the K closest peers to the key (by XOR).
// - After Put, verify every one of those K peers responds with the value.
// - Also verify that the origin responds with the value (local store).
func TestM2_ReplicatesToKClosestIncludingOrigin(t *testing.T) {
	nodes, contacts := m2Cluster(t, 8)
	originIdx := 3
	origin := nodes[originIdx]
	data := []byte("replicate-me")
	key, err := origin.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Determine expected replication set (K peers closest to the key).
	//kPeers := m2KClosestPeersToKey(originIdx, contacts, key)
	kPeers := m2KClosestFromOriginView(t, origin, key)

	// Verify each expected peer actually has the value (direct FIND_VALUE).
	for i := range kPeers {
		peer := kPeers[i]
		if peer.Address == contacts[originIdx].Address {
			continue // origin checked below
		}
		n := m2NodeByAddress(nodes, peer.Address)
		if n == nil {
			t.Fatalf("test mapping error: cannot find node for %s", peer.Address)
		}
		if !m2WaitHasLocalValue(t, n, key, 2*time.Second) {
			t.Fatalf("peer %s did not store replicated value (expected among K closest from origin's view)", peer.Address)
		}
	}

	// Verify origin also stores the value locally.
	if !m2WaitHasLocalValue(t, nodes[originIdx], key, 2*time.Second) {
		t.Fatalf("origin did not store value locally")
	}
}

// TestM2_Get_NotFound
// - Try to Get a random key that was never stored; expect an error.
func TestM2_Get_NotFound(t *testing.T) {
	nodes, _ := m2Cluster(t, 3)

	// Make a random key hex that is unlikely to exist.
	key := m2RandIDHex(t)
	if len(key) != 40 {
		t.Fatalf("bad helper: expected 40 hex chars, got %d", len(key))
	}

	_, _, err := nodes[1].Get(key)
	if err == nil {
		t.Fatalf("Get should have returned not found error")
	}
}

// TestM2_InvalidKeyHexLength
// - Get with a malformed key (wrong length) must error immediately.
func TestM2_InvalidKeyHexLength(t *testing.T) {
	nodes, _ := m2Cluster(t, 3)

	_, _, err := nodes[1].Get("abc") // 3 chars, invalid
	if err == nil {
		t.Fatalf("Get should fail for invalid key hex length")
	}
}

// TestM2_PartialReplication_SomeNodesDown
// - Close one of the K-closest peers before Put.
// - Put must still succeed and Get from other nodes returns the value.
// - The downed peer obviously won't have the value.
func TestM2_PartialReplication_SomeNodesDown(t *testing.T) {
	nodes, contacts := m2Cluster(t, 6)
	origin := nodes[2]

	data := []byte("partial-replication")
	key, err := origin.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Pick one of the K-closest peers and simulate it going down before a second Put.
	// (We do a second Put to attempt replication again with a down node in the set.)
	kPeers := m2KClosestPeersToKey(2, contacts, key)
	if len(kPeers) == 0 {
		t.Skip("cluster too small to pick a peer")
	}
	downPeer := kPeers[0]

	// Find the node instance that matches downPeer.Address and close its network.
	var downNode *Kademlia
	for _, n := range nodes {
		if n.me.Address == downPeer.Address {
			downNode = n
			break
		}
	}
	if downNode == nil {
		t.Fatalf("internal test error: could not match down peer to node")
	}
	_ = downNode.network.Close()
	time.Sleep(50 * time.Millisecond)

	// Re-Put the same data (idempotent) — replication to the down node will fail.
	_, err = origin.Put(data)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}

	// Get from another live node must still succeed.
	val, from, err := nodes[1].Get(key)
	if err != nil || string(val) != string(data) || from == nil {
		t.Fatalf("Get after partial replication failed: val=%q err=%v from=%v", string(val), err, from)
	}

	// Query the down peer via some requester: it should not reply with the value.
	// (sendFindValueTo will timeout or return contacts; both mean "no value".)
	if m2QueryHasValue(t, nodes[0], &downPeer, key) {
		t.Fatalf("down peer unexpectedly responded with value")
	}
}

// TestM2_LocalCachingOnGet
// - Choose a node not in the K-closest set (if possible).
// - Confirm it does NOT have the value right after Put.
// - Call Get(key) on that node; it should fetch and then cache locally.
// - Confirm it now responds with the value directly.
func TestM2_LocalCachingOnGet(t *testing.T) {
	nodes, contacts := m2Cluster(t, 8)
	originIdx := 1
	origin := nodes[originIdx]

	data := []byte("cache-me")
	key, err := origin.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Compute K-closest peers; pick a node NOT in that set (if any).
	kPeers := m2KClosestPeersToKey(originIdx, contacts, key)
	inK := make(map[string]struct{}, len(kPeers))
	for _, p := range kPeers {
		inK[p.Address] = struct{}{}
	}

	// select a candidate outside K set
	var outsiderIdx int = -1
	for i, c := range contacts {
		if i == originIdx {
			continue
		}
		if _, ok := inK[c.Address]; !ok {
			outsiderIdx = i
			break
		}
	}
	if outsiderIdx == -1 {
		t.Skip("all nodes ended up in K set; pick a larger cluster for this test")
	}

	outsider := contacts[outsiderIdx]

	// Before calling Get on outsider, verify it does NOT have the value yet.
	if m2QueryHasValue(t, nodes[0], &outsider, key) {
		t.Fatalf("outsider unexpectedly has value before Get (replication set too large?)")
	}

	// Now outsider performs Get; this should fetch and cache locally.
	val, from, err := nodes[outsiderIdx].Get(key)
	if err != nil || string(val) != string(data) || from == nil {
		t.Fatalf("outsider Get failed: val=%q err=%v from=%v", string(val), err, from)
	}

	// After Get, outsider should respond directly with the value.
	if !m2QueryHasValue(t, nodes[0], &outsider, key) {
		t.Fatalf("outsider did not cache value locally after Get")
	}
}

// TestM2_IdempotentPut
// - Put the same data twice; ensure we don't create weirdness.
// - Verify that the K-closest peers + origin still have it (no change to the expected set).
func TestM2_IdempotentPut(t *testing.T) {
	nodes, contacts := m2Cluster(t, 7)
	originIdx := 4
	origin := nodes[originIdx]

	data := []byte("same-payload")
	key1, err := origin.Put(data)
	if err != nil {
		t.Fatalf("Put#1: %v", err)
	}
	key2, err := origin.Put(data)
	if err != nil {
		t.Fatalf("Put#2: %v", err)
	}
	if key1 != key2 {
		t.Fatalf("Put produced different keys for identical data: %s vs %s", key1, key2)
	}

	exp := m2KClosestFromOriginView(t, origin, key1)

	// Verify each expected peer + origin has the value
	for _, peer := range exp {
		if peer.Address == contacts[originIdx].Address {
			continue // skip origin, checked below
		}
		n := m2NodeByAddress(nodes, peer.Address)
		if n == nil {
			t.Fatalf("test mapping error: cannot find node for %s", peer.Address)
		}
		if !m2WaitHasLocalValue(t, n, key1, 2*time.Second) {
			t.Fatalf("peer %s missing value after idempotent puts (expected among K closest from origin's view)", peer.Address)
		}
	}

	if !m2WaitHasLocalValue(t, nodes[originIdx], key1, 2*time.Second) {
		t.Fatalf("origin missing value after idempotent puts")
	}
}

// TestM2_ConcurrentGets
// - Spawn multiple concurrent Get() calls from different nodes; all must succeed.
func TestM2_ConcurrentGets(t *testing.T) {
	nodes, _ := m2Cluster(t, 8)
	origin := nodes[2]
	data := []byte("mass-get")
	key, err := origin.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	errCh := make(chan error, len(nodes))
	for i := range nodes {
		if i == 2 {
			continue // skip origin to make it interesting
		}
		go func(n *Kademlia) {
			val, _, e := n.Get(key)
			if e != nil {
				errCh <- e
				return
			}
			if string(val) != string(data) {
				errCh <- &mismatchErr{}
				return
			}
			errCh <- nil
		}(nodes[i])
	}

	timeout := time.After(3 * time.Second)
	for pending := 0; pending < len(nodes)-1; pending++ {
		select {
		case e := <-errCh:
			if e != nil {
				t.Fatalf("concurrent Get failed: %v", e)
			}
		case <-timeout:
			t.Fatalf("concurrent Get timed out")
		}
	}
}

// mismatchErr is a tiny typed error to differentiate data mismatch.
type mismatchErr struct{}

func (m *mismatchErr) Error() string { return "value mismatch" }

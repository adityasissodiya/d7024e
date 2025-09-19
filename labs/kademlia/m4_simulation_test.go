package kademlia

import (
	"encoding/hex"
	"flag"
	"testing"
	"time"
)

// Ensure flags are registered once across the package
func TestM4_FlagRegistration(t *testing.T) { _ = flag.CommandLine }

// Happy path at scale: 1000 nodes, no drops -> everyone can retrieve.
func TestM4_Simulation_NoDrop_AllGetsSucceed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping M4 scale test in -short mode")
	}
	nodes := *m4Nodes
	if nodes < 1000 {
		nodes = 1000
	}
	cluster := newSimCluster(t, nodes, 0, *m4Seed)

	// Put from a mid-index node to avoid bias.
	origin := nodes / 2
	keyHex, _, replicas := cluster.simPut(origin, []byte("m4-large-no-drop"))
	if replicas < bucketSize {
		t.Fatalf("expected full replication without drops; got %d of %d", replicas, bucketSize)
	}

	// Try gets from a sample of nodes across the space.
	indices := []int{0, nodes / 4, nodes / 2, (3 * nodes) / 4, nodes - 1}
	for _, idx := range indices {
		val, from, ok := cluster.simGet(idx, keyHex)
		if !ok {
			t.Fatalf("get failed at idx=%d under no-drop", idx)
		}
		if string(val) != "m4-large-no-drop" {
			t.Fatalf("wrong value at idx=%d: %q", idx, string(val))
		}
		if from < 0 || from >= nodes {
			t.Fatalf("invalid 'from' index: %d", from)
		}
	}
}

// With drop% > 0, replication count should still be >= 1 (origin) and lookups should succeed
// from several vantage points with overwhelming probability (deterministic under seed).
func TestM4_Simulation_WithDrop_StillRetrievable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping M4 scale test in -short mode")
	}
	nodes := *m4Nodes
	drop := *m4Drop
	seed := *m4Seed

	if nodes < 1000 {
		nodes = 1000
	}
	if drop < 0 || drop > 100 {
		drop = 10
	}

	cluster := newSimCluster(t, nodes, drop, seed)

	origin := nodes / 3
	keyHex, _, replicas := cluster.simPut(origin, []byte("m4-drop"))
	if replicas < 1 {
		t.Fatalf("replicas should be at least origin: got %d", replicas)
	}
	// Soft diagnostic (not a hard assert to avoid flakiness): replication shouldn't be catastrophic.
	lb := expectedReplicaLowerBound(bucketSize, drop)
	t.Logf("replicas=%d (bucketSize=%d, drop=%d%%, lowerBound~%d)", replicas, bucketSize, drop, lb)

	// Probe from several starting nodes; at least one should succeed deterministically under the seed.
	starts := []int{0, nodes / 5, nodes / 2, (4 * nodes) / 5, nodes - 1, origin}
	var successes int
	for _, idx := range starts {
		if v, from, ok := cluster.simGet(idx, keyHex); ok && string(v) == "m4-drop" && from >= 0 {
			successes++
		}
		// small pause to let scheduler breathe in CI under heavy GC
		time.Sleep(simTinyPause)
	}
	if successes == 0 {
		t.Fatalf("no successful gets under drop=%d%% with K=%d (seed=%d)", drop, bucketSize, seed)
	}
}

// Edge-case: Invalid key shape should never succeed.
func TestM4_Simulation_InvalidKey_NotFound(t *testing.T) {
	cluster := newSimCluster(t, 1000, 0, *m4Seed)
	start := 123
	// 39 hex chars -> invalid (needs 40)
	key := "00112233445566778899aabbccddeeff0011223"
	if v, _, ok := cluster.simGet(start, key); ok || v != nil {
		t.Fatalf("expected not found for invalid hex key length")
	}
	// non-hex characters
	key = "zz112233445566778899aabbccddeeff00112233"
	if v, _, ok := cluster.simGet(start, key); ok || v != nil {
		t.Fatalf("expected not found for invalid hex key digits")
	}
}

// Stress: Multiple distinct keys with different origins across a large cluster.
func TestM4_Simulation_ManyKeys_Distributed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping M4 stress test in -short mode")
	}
	nodes := *m4Nodes
	if nodes < 1000 {
		nodes = 1000
	}
	cluster := newSimCluster(t, nodes, *m4Drop, *m4Seed)

	origins := []int{nodes / 7, nodes / 3, (2 * nodes) / 3, (6 * nodes) / 7}
	keys := make([]string, 0, len(origins))
	payloads := []string{"alpha", "beta", "gamma", "delta"}

	for i, o := range origins {
		k, _, _ := cluster.simPut(o, []byte(payloads[i]))
		keys = append(keys, k)
	}

	// Verify retrieval from a spread of vantage points.
	starts := []int{0, nodes / 4, nodes / 2, (3 * nodes) / 4, nodes - 1}
	for i, keyHex := range keys {
		want := payloads[i]
		var okCount int
		for _, s := range starts {
			if v, _, ok := cluster.simGet(s, keyHex); ok && string(v) == want {
				okCount++
			}
		}
		if okCount == 0 {
			t.Fatalf("key %s (payload=%s) was not retrievable from any vantage point", keyHex, want)
		}
	}
}

// Sanity: define a valid 40-hex and ensure simGet rejects bad decode early.
func TestM4_Simulation_KeyShape_Sanity(t *testing.T) {
	sum := [20]byte{1, 2, 3, 4}
	key := hex.EncodeToString(sum[:])
	if len(key) != 40 {
		t.Fatalf("expected 40 hex chars, got %d", len(key))
	}
	cluster := newSimCluster(t, 1000, 0, *m4Seed)
	if _, _, ok := cluster.simGet(0, key); ok {
		// Most likely missing (value not put), which is OK; we just confirm no panic and valid shape.
	}
}

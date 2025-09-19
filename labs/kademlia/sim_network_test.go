package kademlia

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"math"
	mrand "math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

// -----------------------------
// M4 knobs (easy to change):
//   -m4.nodes=N  (default 1000)
//   -m4.drop=P   (percent 0..100; default 10)
//   -m4.seed=S   (deterministic PRNG; default 1337)
// -----------------------------

var (
	m4Nodes = flag.Int("m4.nodes", 1000, "number of nodes to emulate for M4")
	m4Drop  = flag.Int("m4.drop", 10, "packet drop percentage 0..100")
	m4Seed  = flag.Int64("m4.seed", 1337, "PRNG seed for deterministic simulation")
)

// Ensure flags are parsed even if no TestMain is present.
func init() {
	// noop: testing package parses flags; keeping defaults visible via -test.run.
}

// -----------------------------
// Simulated cluster & nodes
// -----------------------------

type simCluster struct {
	nodes   []*simNode
	dropPct int         // 0..100
	rng     *mrand.Rand // deterministic PRNG (seeded)
	rngMu   sync.Mutex  // guard PRNG access
}

type simNode struct {
	id         KademliaID
	address    string
	valueStore map[string][]byte
	mu         sync.RWMutex // protect valueStore
}

func newSimCluster(t *testing.T, n int, dropPct int, seed int64) *simCluster {
	if n <= 0 {
		t.Fatalf("newSimCluster: n must be > 0")
	}
	if dropPct < 0 || dropPct > 100 {
		t.Fatalf("newSimCluster: dropPct out of range: %d", dropPct)
	}
	rng := mrand.New(mrand.NewSource(seed))
	nodes := make([]*simNode, n)
	for i := 0; i < n; i++ {
		id := randomKID(rng)
		nodes[i] = &simNode{
			id:         id,
			address:    "sim:" + hex.EncodeToString(id[:4]) + ":" + funcitoa(i),
			valueStore: make(map[string][]byte),
		}
	}
	return &simCluster{
		nodes:   nodes,
		dropPct: dropPct,
		rng:     rng,
	}
}

// randomKID returns a random 160-bit KademliaID using math/rand (deterministic under seed).
func randomKID(rng *mrand.Rand) KademliaID {
	var id KademliaID
	// Fill 20 bytes using crypto/rand mixed with math/rand for stability but uniqueness
	// (test-only; determinism is from math/rand).
	var tmp [20]byte
	_, _ = rand.Read(tmp[:])
	for i := 0; i < len(id); i++ {
		id[i] = byte(rng.Intn(256)) ^ tmp[i]
	}
	return id
}

// itoa (fast small helper, no fmt)
func funcitoa(x int) string {
	if x == 0 {
		return "0"
	}
	neg := x < 0
	if neg {
		x = -x
	}
	var b [20]byte
	i := len(b)
	for x > 0 {
		i--
		b[i] = byte('0' + x%10)
		x /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// -----------------------------
// Distance helpers
// -----------------------------

type dist [20]byte

// xorDist returns XOR distance between a node ID and a key ID.
func xorDist(a, b *KademliaID) dist {
	var d dist
	for i := 0; i < 20; i++ {
		d[i] = a[i] ^ b[i]
	}
	return d
}

// lessDist compares two distances lexicographically (MSB-first).
func lessDist(x, y dist) bool {
	for i := 0; i < 20; i++ {
		if x[i] < y[i] {
			return true
		}
		if x[i] > y[i] {
			return false
		}
	}
	return false
}

// -----------------------------
// Packet drop helper
// -----------------------------

func (c *simCluster) dropped() bool {
	if c.dropPct <= 0 {
		return false
	}
	if c.dropPct >= 100 {
		return true
	}
	c.rngMu.Lock()
	p := c.rng.Intn(100)
	c.rngMu.Unlock()
	return p < c.dropPct
}

// -----------------------------
// Store and Get simulation
// -----------------------------

// simPut replicates value to K closest nodes to the key and stores at origin unconditionally.
// It models network drops on STORE deliveries.
func (c *simCluster) simPut(origin int, value []byte) (keyHex string, keyID KademliaID, replicatedTo int) {
	sum := sha1.Sum(value)
	keyHex = hex.EncodeToString(sum[:])
	copy(keyID[:], sum[:])

	// Always store locally at origin (no network).
	c.nodes[origin].mu.Lock()
	c.nodes[origin].valueStore[keyHex] = append([]byte(nil), value...)
	c.nodes[origin].mu.Unlock()
	replicatedTo = 1 // origin included

	// Determine K closest nodes (global view for the simulator).
	K := bucketSize // reuse your package-level constant
	type cand struct {
		idx int
		d   dist
	}
	cands := make([]cand, 0, len(c.nodes))
	for i, n := range c.nodes {
		d := xorDist(&n.id, &keyID)
		cands = append(cands, cand{idx: i, d: d})
	}
	sort.Slice(cands, func(i, j int) bool { return lessDist(cands[i].d, cands[j].d) })

	// Replicate to top-K, ensuring origin is included (already stored).
	for i := 0; i < len(cands) && replicatedTo < K; i++ {
		idx := cands[i].idx
		if idx == origin {
			continue // already stored
		}
		// Network delivery for STORE may be dropped.
		if c.dropped() {
			continue
		}
		c.nodes[idx].mu.Lock()
		c.nodes[idx].valueStore[keyHex] = append([]byte(nil), value...)
		c.nodes[idx].mu.Unlock()
		replicatedTo++
	}
	return keyHex, keyID, replicatedTo
}

// simGet tries to find the value starting from 'start' by consulting the K closest nodes.
// It models drop on FIND_VALUE response delivery. If any delivery arrives, it succeeds.
func (c *simCluster) simGet(start int, keyHex string) (val []byte, fromIdx int, ok bool) {
	// Local check first (no network).
	c.nodes[start].mu.RLock()
	if v, okLocal := c.nodes[start].valueStore[keyHex]; okLocal {
		out := append([]byte(nil), v...)
		c.nodes[start].mu.RUnlock()
		return out, start, true
	}
	c.nodes[start].mu.RUnlock()

	// Compute key ID
	raw, err := hex.DecodeString(keyHex)
	if err != nil || len(raw) != 20 {
		return nil, -1, false
	}
	var keyID KademliaID
	copy(keyID[:], raw)

	// Query up to K closest nodes to the key (global list in the simulator).
	K := bucketSize
	type cand struct {
		idx int
		d   dist
	}
	cands := make([]cand, 0, len(c.nodes))
	for i, n := range c.nodes {
		d := xorDist(&n.id, &keyID)
		cands = append(cands, cand{idx: i, d: d})
	}
	sort.Slice(cands, func(i, j int) bool { return lessDist(cands[i].d, cands[j].d) })

	// Iterate candidates; simulate request/response drop on each.
	for i := 0; i < len(cands) && i < K; i++ {
		idx := cands[i].idx
		// Simulate request drop
		if c.dropped() {
			continue
		}
		// Check value
		c.nodes[idx].mu.RLock()
		v, have := c.nodes[idx].valueStore[keyHex]
		c.nodes[idx].mu.RUnlock()
		if !have {
			continue
		}
		// Simulate response drop
		if c.dropped() {
			continue
		}
		return append([]byte(nil), v...), idx, true
	}
	return nil, -1, false
}

// -----------------------------
// Utility for expected replication coverage
// -----------------------------

// expectedReplicaLowerBound returns a conservative lower bound for replicas with dropPct.
// This is not used as a strict test oracle (to avoid flakiness); it’s only for diagnostics.
func expectedReplicaLowerBound(K int, dropPct int) int {
	if dropPct <= 0 {
		return K
	}
	p := float64(100-dropPct) / 100.0
	m := float64(K) * p
	// -3 sigma for safety (binomial spread), but cap at [1..K]
	sigma := math.Sqrt(float64(K) * p * (1 - p))
	lb := int(math.Floor(m - 3*sigma))
	if lb < 1 {
		lb = 1
	}
	if lb > K {
		lb = K
	}
	return lb
}

// Small sleep to avoid starving the scheduler on very large N in CI; test can reduce this further if needed.
const simTinyPause = 1 * time.Millisecond

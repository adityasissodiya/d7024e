package kademlia

// kademlia.go: M1 node state, Join, and iterative FIND_NODE
// NOTE: variable names preserved: "routingTable" and "candidates".

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Kademlia struct {
	me           Contact
	routingTable *RoutingTable
	network      *Network

	alpha      int
	timeoutRPC time.Duration

	// M2 local store
	storeMu    sync.RWMutex
	valueStore map[string][]byte // keyHex -> value

	// ---- M2.5+: maintenance ----
	// Track keys we ORIGINATED via Put(); only those are periodically republished.
	originMu   sync.RWMutex
	originKeys map[string]struct{}
	// Cooperative stop for the republisher goroutine.
	republishStop     chan struct{}
	republishInterval time.Duration
}

// NewKademlia creates a node bound to ip:port. Keep your Contact constructor.
func NewKademlia(me Contact, ip string, port int) (*Kademlia, error) {
	kademlia := &Kademlia{
		me:            me,
		alpha:         3,
		timeoutRPC:    800 * time.Millisecond,
		originKeys:    make(map[string]struct{}),
		republishStop: make(chan struct{}),
		// NOTE: Kademlia paper uses ~24h; for lab/demo you can shorten.
		republishInterval: 15 * time.Minute,
	}
	kademlia.routingTable = NewRoutingTable(me)

	netw, err := NewNetwork(kademlia, ip, port)
	if err != nil {
		return nil, err
	}
	// Start background republisher AFTER network is ready.
	go kademlia.republisher()
	kademlia.network = netw
	// Wire LRU-eviction liveness probe: ping with the same timeout used elsewhere.
	kademlia.routingTable.SetPingFunc(func(c Contact) bool {
		return kademlia.network.PingWait(&c, kademlia.timeoutRPC)
	})
	return kademlia, nil
}

func (kademlia *Kademlia) Close() error {
	// Stop the republisher loop.
	if kademlia.republishStop != nil {
		close(kademlia.republishStop)
	}
	if kademlia.network != nil {
		return kademlia.network.Close()
	}
	return nil
}

// Join the network via a known bootstrap node.
// 1) PING the bootstrap
// 2) Iterative lookup for our own ID to populate routing table
func (kademlia *Kademlia) Join(bootstrap *Contact) error {
	if bootstrap == nil || bootstrap.ID == nil || bootstrap.Address == "" {
		return fmt.Errorf("invalid bootstrap")
	}
	kademlia.network.SendPingMessage(bootstrap)

	// Then the canonical join step: lookup our own ID
	self := Contact{ID: kademlia.me.ID}
	kademlia.LookupContact(&self)
	return nil
}

// LookupContact performs an iterative node lookup for target.ID.
// It updates routingTable; get results via routingTable.FindClosestContacts(target.ID, n).
func (kademlia *Kademlia) LookupContact(target *Contact) {
	if target == nil || target.ID == nil {
		return
	}
	// Initial seed
	candidates := kademlia.routingTable.FindClosestContacts(target.ID, bucketSize*3)

	visited := make(map[string]struct{})

	// Select next α unvisited peers closest to target
	nextBatch := func() []Contact {
		candidates = kademlia.routingTable.FindClosestContacts(target.ID, 1024)
		batch := make([]Contact, 0, kademlia.alpha)
		for _, contact := range candidates {
			if len(batch) >= kademlia.alpha {
				break
			}
			if contact.Address == "" {
				continue
			}
			if _, seen := visited[contact.Address]; seen {
				continue
			}
			visited[contact.Address] = struct{}{}
			batch = append(batch, contact)
		}
		return batch
	}

	var lastBest *KademliaID

	for {
		batch := nextBatch()
		if len(batch) == 0 {
			break
		}

		type result struct{}
		results := make(chan result, len(batch))

		for i := range batch {
			peer := batch[i]
			go func() {
				// Ask "peer" for contacts close to "target"
				_, _ = kademlia.network.SendFindContactMessageTo(&peer, target)
				results <- result{}
			}()
		}

		for i := 0; i < len(batch); i++ {
			<-results
		}

		// Convergence check: if best known contact didn't improve, stop
		closestNow := kademlia.routingTable.FindClosestContacts(target.ID, 1)
		if len(closestNow) == 0 {
			break
		}
		best := closestNow[0].ID
		if lastBest != nil && !best.CalcDistance(target.ID).Less(lastBest.CalcDistance(target.ID)) {
			break
		}
		lastBest = best
	}

	// Optional: stable ordering for determinism in tests/demos
	final := kademlia.routingTable.FindClosestContacts(target.ID, bucketSize)
	sort.SliceStable(final, func(i, j int) bool {
		return final[i].ID.CalcDistance(target.ID).Less(final[j].ID.CalcDistance(target.ID))
	})
}

// ClosestContacts returns up to 'count' closest contacts to 'target' from this node's view.
func (kademlia *Kademlia) ClosestContacts(target *KademliaID, count int) []Contact {
	return kademlia.routingTable.FindClosestContacts(target, count)
}

// ---- M2: local store helpers ----

func (kademlia *Kademlia) keyFromData(data []byte) (keyHex string, id *KademliaID) {
	sum := sha1.Sum(data) // 20 bytes
	keyHex = hex.EncodeToString(sum[:])
	var kid KademliaID
	copy(kid[:], sum[:])
	return keyHex, &kid
}

func (kademlia *Kademlia) storeLocal(keyHex string, value []byte) {
	kademlia.storeMu.Lock()
	if kademlia.valueStore == nil { // lazy init to avoid nil map panics
		kademlia.valueStore = make(map[string][]byte)
	}
	v := make([]byte, len(value)) // copy to avoid aliasing
	copy(v, value)
	kademlia.valueStore[keyHex] = v
	kademlia.storeMu.Unlock()
}

func (kademlia *Kademlia) loadLocal(keyHex string) ([]byte, bool) {
	kademlia.storeMu.RLock()
	if kademlia.valueStore == nil { // handle nil map safely
		kademlia.storeMu.RUnlock()
		return nil, false
	}
	v, ok := kademlia.valueStore[keyHex]
	kademlia.storeMu.RUnlock()
	if !ok {
		return nil, false
	}
	out := make([]byte, len(v)) // return a copy
	copy(out, v)
	return out, true
}

// ---- M2: public API ----

// Store(data) per skeleton (no return). Replicates to K closest nodes.
func (kademlia *Kademlia) Store(data []byte) {
	_, _ = kademlia.Put(data) // delegate to returning variant; ignore key here
}

// Put returns the key (hex) and any error; use this in tests/CLI.
func (kademlia *Kademlia) Put(data []byte) (string, error) {
	keyHex, keyID := kademlia.keyFromData(data)

	// Always store at the origin immediately.
	kademlia.storeLocal(keyHex, data)
	fmt.Printf("[PUT] key=%s me=%s stored_local\n", keyHex, kademlia.me.Address)
	// Find K closest nodes to the key (iterative lookup).
	//target := Contact{ID: keyID}
	//kademlia.LookupContact(&target)

	//contacts := kademlia.routingTable.FindClosestContacts(keyID, bucketSize)
	//sort.SliceStable(contacts, func(i, j int) bool {
	//	return contacts[i].ID.CalcDistance(keyID).Less(contacts[j].ID.CalcDistance(keyID))
	//})

	// Replicate to K closest peers (skip self).
	//for _, c := range contacts {
	//	if c.Address == kademlia.me.Address {
	//		continue
	//	}
	//	_ = kademlia.network.sendStoreTo(&c, keyHex, data, kademlia.timeoutRPC)
	//}

	// Mark as an origin key so the republisher will maintain it over time.
	kademlia.originMu.Lock()
	kademlia.originKeys[keyHex] = struct{}{}
	kademlia.originMu.Unlock()

	// Initial placement to CURRENT K-closest (lookup embeds table refresh).
	kademlia.replicateToClosest(keyHex, keyID, data)

	return keyHex, nil
}

// LookupData per skeleton (no return). Wrapper over Get.
func (kademlia *Kademlia) LookupData(hash string) {
	_, _, _ = kademlia.Get(hash)
}

// Get performs FIND_VALUE iterative lookup.
// Returns the value (if found), and the contact that returned it.
func (kademlia *Kademlia) Get(keyHex string) ([]byte, *Contact, error) {

	fmt.Printf("[GET] key=%s me=%s\n", keyHex, kademlia.me.Address)

	// quick local check
	if v, ok := kademlia.loadLocal(keyHex); ok {
		me := kademlia.me
		fmt.Printf("[GET] local_hit=%v\n", ok)
		return v, &me, nil
	}

	// Treat key as an ID for distance/candidate selection
	if len(keyHex) != 40 {
		return nil, nil, fmt.Errorf("invalid key hex length")
	}
	b, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, nil, err
	}
	var keyID KademliaID
	copy(keyID[:], b)

	// Seed candidates
	candidates := kademlia.routingTable.FindClosestContacts(&keyID, bucketSize*3)
	visited := make(map[string]struct{})
	// Track which peers we actually queried (for path caching later).
	queried := make([]Contact, 0, 64)
	nextBatch := func() []Contact {
		// refresh view from table each round
		candidates = kademlia.routingTable.FindClosestContacts(&keyID, 1024)
		batch := make([]Contact, 0, kademlia.alpha)
		for _, contact := range candidates {
			if len(batch) >= kademlia.alpha {
				break
			}
			if contact.Address == "" {
				continue
			}
			if _, seen := visited[contact.Address]; seen {
				continue
			}
			visited[contact.Address] = struct{}{}
			batch = append(batch, contact)
		}
		return batch
	}

	var lastBest *KademliaID

	for {

		batch := nextBatch()
		if len(batch) == 0 {
			break
		}

		fmt.Printf("[GET] batch size=%d candidates querying:\n", len(batch))
		for _, c := range batch {
			d := c.ID.CalcDistance(&keyID)
			fmt.Printf("  -> %s dist=%s\n", c.Address, d.String())
		}

		// Record which nodes we're querying this round (for path caching).
		queried = append(queried, batch...)

		type res struct {
			value    []byte
			contacts []Contact
			from     *Contact
			err      error
		}
		ch := make(chan res, len(batch))

		for i := range batch {
			peer := batch[i]
			go func(p Contact) {
				val, cons, e := kademlia.network.sendFindValueTo(&p, keyHex, kademlia.timeoutRPC)
				if e == nil && len(val) > 0 {
					// Early success
					ch <- res{value: val, from: &p}
					return
				}
				ch <- res{contacts: cons, from: &p, err: e}
			}(peer)
		}

		gotValue := false
		var val []byte
		var src *Contact

		for i := 0; i < len(batch); i++ {
			r := <-ch
			if r.err == nil && len(r.value) > 0 {
				val = r.value
				src = r.from
				gotValue = true
			}
			// network.sendFindValueTo already learned contacts into the table
		}
		if gotValue {
			// cache locally
			kademlia.storeLocal(keyHex, val)

			// -------- PATH CACHING --------
			// Also STORE the value at the *closest* node that we actually contacted
			// (excluding the source that had the value and ourselves).
			// This helps seed the correct region even if the publisher hasn't republished yet.
			bestIdx := -1
			for i := range queried {
				q := queried[i]
				if src != nil && q.Address == src.Address {
					continue // the responder already has it
				}
				if q.Address == kademlia.me.Address {
					continue // we just cached locally
				}
				if bestIdx == -1 {
					bestIdx = i
					continue
				}
				// Choose the contact closer to the keyID.
				if q.ID.CalcDistance(&keyID).Less(queried[bestIdx].ID.CalcDistance(&keyID)) {
					bestIdx = i
				}
			}
			if bestIdx >= 0 {
				_ = kademlia.network.sendStoreTo(&queried[bestIdx], keyHex, val, kademlia.timeoutRPC)
			}

			fmt.Printf("[GET] GOT value from=%s len=%d\n", src.Address, len(val))
			fmt.Printf("[GET] PATH-CACHE store to %s\n", queried[bestIdx].Address)

			return val, src, nil
		}

		// Convergence: stop when best contact doesn't improve
		closestNow := kademlia.routingTable.FindClosestContacts(&keyID, 1)
		if len(closestNow) == 0 {
			break
		}
		best := closestNow[0].ID
		if lastBest != nil && !best.CalcDistance(&keyID).Less(lastBest.CalcDistance(&keyID)) {
			break
		}
		lastBest = best
	}

	return nil, nil, fmt.Errorf("not found")
}

// replicateToClosest finds the CURRENT K closest nodes to keyID and sends STORE.
// Shared by Put() (initial placement) and the periodic republisher.
func (kademlia *Kademlia) replicateToClosest(keyHex string, keyID *KademliaID, value []byte) {
	// High-level trace so you can correlate init vs republish calls.
	fmt.Printf("[REPLICATE] key=%s me=%s start\n", keyHex, kademlia.me.Address)
	if keyID == nil || len(keyHex) != 40 || len(value) == 0 {
		return
	}
	// Refresh view of the network around this key to avoid stale placement.
	target := Contact{ID: keyID}
	kademlia.LookupContact(&target)

	contacts := kademlia.routingTable.FindClosestContacts(keyID, bucketSize)
	sort.SliceStable(contacts, func(i, j int) bool {
		return contacts[i].ID.CalcDistance(keyID).Less(contacts[j].ID.CalcDistance(keyID))
	})
	// Optional: show the first few candidates + their XOR distance to the key.
	for i, c := range contacts {
		if i >= 8 {
			break
		}
		fmt.Printf("[REPLICATE] candidate[%d]=%s dist=%s\n",
			i, c.Address, c.ID.CalcDistance(keyID).String())
	}
	for _, c := range contacts {
		if c.Address == kademlia.me.Address {
			continue // we already stored locally
		}
		// <-- this is the print you asked about, now in the right place
		fmt.Printf("[REPLICATE] -> %s (closest to key)\n", c.Address)
		// Fire-and-forget semantics are OK; we tolerate timeouts.
		_ = kademlia.network.sendStoreTo(&c, keyHex, value, kademlia.timeoutRPC)
	}
}

// republisher ticks forever (until Close) and republishes *origin* keys
// to the CURRENT K closest peers, ensuring newly joined closer nodes receive them.
func (kademlia *Kademlia) republisher() {
	ticker := time.NewTicker(kademlia.republishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			kademlia.republishOwnedKeys()
		case <-kademlia.republishStop:
			return
		}
	}
}

func (kademlia *Kademlia) republishOwnedKeys() {
	// Snapshot list of origin keys under lock; read values safely.
	kademlia.originMu.RLock()
	keys := make([]string, 0, len(kademlia.originKeys))
	for k := range kademlia.originKeys {
		keys = append(keys, k)
	}
	kademlia.originMu.RUnlock()

	for _, keyHex := range keys {
		// Read value (copy) under RLock.
		kademlia.storeMu.RLock()
		val, ok := kademlia.valueStore[keyHex]
		if !ok || len(val) == 0 {
			kademlia.storeMu.RUnlock()
			continue
		}
		v := make([]byte, len(val))
		copy(v, val)
		kademlia.storeMu.RUnlock()

		// Decode hex -> KademliaID for distance calcs.
		b, err := hex.DecodeString(keyHex)
		if err != nil || len(b) != IDLength {
			continue
		}
		var keyID KademliaID
		copy(keyID[:], b)

		kademlia.replicateToClosest(keyHex, &keyID, v)
	}
}

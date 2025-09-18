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
}

// NewKademlia creates a node bound to ip:port. Keep your Contact constructor.
func NewKademlia(me Contact, ip string, port int) (*Kademlia, error) {
	kademlia := &Kademlia{
		me:         me,
		alpha:      3,
		timeoutRPC: 800 * time.Millisecond,
	}
	kademlia.routingTable = NewRoutingTable(me)

	netw, err := NewNetwork(kademlia, ip, port)
	if err != nil {
		return nil, err
	}
	kademlia.network = netw
	return kademlia, nil
}

func (kademlia *Kademlia) Close() error {
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
	if kademlia.valueStore == nil { // ✅ lazy init to avoid nil map panics
		kademlia.valueStore = make(map[string][]byte)
	}
	v := make([]byte, len(value)) // copy to avoid aliasing
	copy(v, value)
	kademlia.valueStore[keyHex] = v
	kademlia.storeMu.Unlock()
}

func (kademlia *Kademlia) loadLocal(keyHex string) ([]byte, bool) {
	kademlia.storeMu.RLock()
	if kademlia.valueStore == nil { // ✅ handle nil map safely
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

	// ✅ Always store at the origin immediately.
	kademlia.storeLocal(keyHex, data)

	// Find K closest nodes to the key (iterative lookup).
	target := Contact{ID: keyID}
	kademlia.LookupContact(&target)

	contacts := kademlia.routingTable.FindClosestContacts(keyID, bucketSize)
	sort.SliceStable(contacts, func(i, j int) bool {
		return contacts[i].ID.CalcDistance(keyID).Less(contacts[j].ID.CalcDistance(keyID))
	})

	// Replicate to K closest peers (skip self).
	for _, c := range contacts {
		if c.Address == kademlia.me.Address {
			continue
		}
		_ = kademlia.network.sendStoreTo(&c, keyHex, data, kademlia.timeoutRPC)
	}

	return keyHex, nil
}

// LookupData per skeleton (no return). Wrapper over Get.
func (kademlia *Kademlia) LookupData(hash string) {
	_, _, _ = kademlia.Get(hash)
}

// Get performs FIND_VALUE iterative lookup.
// Returns the value (if found), and the contact that returned it.
func (kademlia *Kademlia) Get(keyHex string) ([]byte, *Contact, error) {
	// quick local check
	if v, ok := kademlia.loadLocal(keyHex); ok {
		me := kademlia.me
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

// Leave M2/M3 placeholders intact.
//func (kademlia *Kademlia) LookupData(hash string) { /* TODO */ }
//func (kademlia *Kademlia) Store(data []byte)      { /* TODO */ }

package kademlia

import (
	"sync"
)

const bucketSize = 20

// RoutingTable definition
// keeps a refrence contact of me and an array of buckets
type RoutingTable struct {
	me      Contact
	buckets [IDLength * 8]*bucket
	mu      sync.RWMutex
	// Called outside the lock to test liveness of an LRU contact when a bucket is full.
	pingFunc func(Contact) bool
}

// NewRoutingTable returns a new instance of a RoutingTable
func NewRoutingTable(me Contact) *RoutingTable {
	routingTable := &RoutingTable{}
	for i := 0; i < IDLength*8; i++ {
		routingTable.buckets[i] = newBucket()
	}
	routingTable.me = me
	return routingTable
}

// SetPingFunc wires a liveness probe used by the eviction policy.
func (routingTable *RoutingTable) SetPingFunc(pf func(Contact) bool) {
	routingTable.mu.Lock()
	routingTable.pingFunc = pf
	routingTable.mu.Unlock()
}

// AddContact add a new contact to the correct Bucket
func (routingTable *RoutingTable) AddContact(contact Contact) {
	//routingTable.mu.Lock()
	//defer routingTable.mu.Unlock()
	//bucketIndex := routingTable.getBucketIndex(contact.ID)
	//bucket := routingTable.buckets[bucketIndex]
	//bucket.AddContact(contact)
	if contact.ID == nil {
		return
	}
	// Ignore self.
	if routingTable.me.ID != nil && routingTable.me.ID.Equals(contact.ID) {
		return
	}

	bucketIndex := routingTable.getBucketIndex(contact.ID)

	// ---- Phase 1: decide under lock (find existing / space / LRU) ----
	routingTable.mu.Lock()
	b := routingTable.buckets[bucketIndex]
	// If already present, move-to-front (most-recent) and return.
	for e := b.list.Front(); e != nil; e = e.Next() {
		if e.Value.(Contact).ID.Equals(contact.ID) {
			b.list.MoveToFront(e)
			routingTable.mu.Unlock()
			return
		}
	}
	// If space exists, just insert at front.
	if b.list.Len() < bucketSize {
		b.list.PushFront(contact)
		routingTable.mu.Unlock()
		return
	}

	// Full: capture *current* LRU (least-recent) and release lock to ping it.
	lruElt := b.list.Back()
	lru := lruElt.Value.(Contact)
	routingTable.mu.Unlock()

	// ---- Phase 2: liveness check OUTSIDE the lock ----
	alive := false
	if routingTable.pingFunc != nil {
		alive = routingTable.pingFunc(lru)
	}

	// ---- Phase 3: re-acquire and mutate bucket based on liveness ----
	routingTable.mu.Lock()
	defer routingTable.mu.Unlock()
	b = routingTable.buckets[bucketIndex]

	if !alive {
		// Evict the LRU (if still there), add the new contact at front.
		for e := b.list.Back(); e != nil; e = e.Prev() {
			if e.Value.(Contact).ID.Equals(lru.ID) {
				b.list.Remove(e)
				break
			}
		}
		b.list.PushFront(contact)
		return
	}

	// LRU responded: keep it (and mark recently seen), drop the newcomer,
	// but remember the new contact in the replacement cache.
	for e := b.list.Back(); e != nil; e = e.Prev() {
		if e.Value.(Contact).ID.Equals(lru.ID) {
			b.list.MoveToFront(e)
			break
		}
	}
	b.addReplacement(contact)

}

// FindClosestContacts finds the count closest Contacts to the target in the RoutingTable
func (routingTable *RoutingTable) FindClosestContacts(target *KademliaID, count int) []Contact {
	routingTable.mu.RLock()
	defer routingTable.mu.RUnlock()
	var candidates ContactCandidates
	bucketIndex := routingTable.getBucketIndex(target)
	bucket := routingTable.buckets[bucketIndex]

	candidates.Append(bucket.GetContactAndCalcDistance(target))

	for i := 1; (bucketIndex-i >= 0 || bucketIndex+i < IDLength*8) && candidates.Len() < count; i++ {
		if bucketIndex-i >= 0 {
			bucket = routingTable.buckets[bucketIndex-i]
			candidates.Append(bucket.GetContactAndCalcDistance(target))
		}
		if bucketIndex+i < IDLength*8 {
			bucket = routingTable.buckets[bucketIndex+i]
			candidates.Append(bucket.GetContactAndCalcDistance(target))
		}
	}

	candidates.Sort()

	if count > candidates.Len() {
		count = candidates.Len()
	}

	return candidates.GetContacts(count)
}

// getBucketIndex get the correct Bucket index for the KademliaID
func (routingTable *RoutingTable) getBucketIndex(id *KademliaID) int {
	distance := id.CalcDistance(routingTable.me.ID)
	for i := 0; i < IDLength; i++ {
		for j := 0; j < 8; j++ {
			if (distance[i]>>uint8(7-j))&0x1 != 0 {
				return i*8 + j
			}
		}
	}

	return IDLength*8 - 1
}

package kademlia

import (
	"encoding/hex"
	"fmt"
	"testing"
)

// FIXME: This test doesn't actually test anything. There is only one assertion
// that is included as an example.

func TestRoutingTable(t *testing.T) {
	rt := NewRoutingTable(NewContact(NewKademliaID("FFFFFFFF00000000000000000000000000000000"), "localhost:8000"))

	rt.AddContact(NewContact(NewKademliaID("FFFFFFFF00000000000000000000000000000000"), "localhost:8001"))
	rt.AddContact(NewContact(NewKademliaID("1111111100000000000000000000000000000000"), "localhost:8002"))
	rt.AddContact(NewContact(NewKademliaID("1111111200000000000000000000000000000000"), "localhost:8002"))
	rt.AddContact(NewContact(NewKademliaID("1111111300000000000000000000000000000000"), "localhost:8002"))
	rt.AddContact(NewContact(NewKademliaID("1111111400000000000000000000000000000000"), "localhost:8002"))
	rt.AddContact(NewContact(NewKademliaID("2111111400000000000000000000000000000000"), "localhost:8002"))

	contacts := rt.FindClosestContacts(NewKademliaID("2111111400000000000000000000000000000000"), 20)
	for i := range contacts {
		fmt.Println(contacts[i].String())
	}

	// TODO: This is just an example. Make more meaningful assertions.
	if len(contacts) != 6 {
		t.Fatalf("Expected 6 contacts but instead got %d", len(contacts))
	}
}

// ---- helpers ----

func zeroIDHex() string {
	// 20 bytes of zero => 40 hex chars
	return "0000000000000000000000000000000000000000"
}

// All contacts made by this helper land in the SAME bucket when me.ID == 0.
// We set the first byte to 0x80 so the MSB is the first differing bit (bucket 0).
func sameBucketID(i int) *KademliaID {
	b := make([]byte, IDLength)
	b[0] = 0x80             // ensures bucket index 0 vs me=0
	b[IDLength-1] = byte(i) // make each contact unique
	return NewKademliaID(hex.EncodeToString(b))
}

func makeContact(i int) Contact {
	return NewContact(sameBucketID(i), fmt.Sprintf("127.0.0.1:%d", 10000+i))
}

func containsAddr(contacts []Contact, addr string) bool {
	for _, c := range contacts {
		if c.Address == addr {
			return true
		}
	}
	return false
}

// Choose any target in the same bucket (distance ordering doesn't matter for set-membership checks).
func targetID() *KademliaID { return sameBucketID(123) }

// ---- tests ----

// Fill one bucket to capacity, then add a new contact while the LRU is "dead":
// expect: LRU evicted, new contact inserted, total size remains bucketSize.
func TestRoutingTable_EvictsDeadLRUAndInsertsNew(t *testing.T) {
	me := NewContact(NewKademliaID(zeroIDHex()), "127.0.0.1:9999")
	rt := NewRoutingTable(me)

	// LRU probe returns false (dead)
	rt.SetPingFunc(func(c Contact) bool { return false })

	// Fill the bucket to capacity with contacts all mapping to the same bucket.
	addrs := make([]string, 0, bucketSize)
	for i := 0; i < bucketSize; i++ {
		c := makeContact(i)
		addrs = append(addrs, c.Address)
		rt.AddContact(c)
	}

	// The LRU should be the earliest inserted (i==0) due to PushFront semantics.
	lruAddr := addrs[0]

	// Add a new contact to trigger eviction of the (dead) LRU.
	newC := makeContact(200)
	newAddr := newC.Address
	rt.AddContact(newC)

	// Read back the contacts (distance-sorted); check set membership and size.
	got := rt.FindClosestContacts(targetID(), 100)
	if len(got) != bucketSize {
		t.Fatalf("expected bucket size %d, got %d", bucketSize, len(got))
	}
	if containsAddr(got, lruAddr) {
		t.Fatalf("expected LRU %q to be evicted (dead), but it's still present", lruAddr)
	}
	if !containsAddr(got, newAddr) {
		t.Fatalf("expected new contact %q to be inserted after evicting dead LRU", newAddr)
	}
}

// Fill one bucket to capacity, then add a new contact while the LRU is "alive":
// expect: LRU kept, new contact NOT inserted (goes to replacement cache), size unchanged.
func TestRoutingTable_KeepsAliveLRUAndDropsNewToReplacement(t *testing.T) {
	me := NewContact(NewKademliaID(zeroIDHex()), "127.0.0.1:9999")
	rt := NewRoutingTable(me)

	// LRU probe returns true (alive)
	rt.SetPingFunc(func(c Contact) bool { return true })

	// Fill the bucket to capacity.
	addrs := make([]string, 0, bucketSize)
	for i := 0; i < bucketSize; i++ {
		c := makeContact(i)
		addrs = append(addrs, c.Address)
		rt.AddContact(c)
	}

	// The LRU (earliest inserted) should remain; newcomer must NOT enter the main list.
	lruAddr := addrs[0]
	newC := makeContact(201)
	newAddr := newC.Address
	rt.AddContact(newC)

	got := rt.FindClosestContacts(targetID(), 100)
	if len(got) != bucketSize {
		t.Fatalf("expected bucket size %d, got %d", bucketSize, len(got))
	}
	if !containsAddr(got, lruAddr) {
		t.Fatalf("expected LRU %q to remain (alive), but it's missing", lruAddr)
	}
	if containsAddr(got, newAddr) {
		t.Fatalf("expected new contact %q to be dropped from main list (alive LRU), but it was inserted", newAddr)
	}
}

// Sanity: re-adding an existing contact should move it toward MRU without changing size or membership.
func TestRoutingTable_MoveToFrontOnSeenAgain(t *testing.T) {
	me := NewContact(NewKademliaID(zeroIDHex()), "127.0.0.1:9999")
	rt := NewRoutingTable(me)
	rt.SetPingFunc(func(c Contact) bool { return true })

	for i := 0; i < bucketSize; i++ {
		rt.AddContact(makeContact(i))
	}
	// Re-add an existing contact; membership and size must remain the same.
	again := makeContact(bucketSize / 2)
	rt.AddContact(again)

	got := rt.FindClosestContacts(targetID(), 100)
	if len(got) != bucketSize {
		t.Fatalf("expected bucket size %d, got %d", bucketSize, len(got))
	}
	if !containsAddr(got, again.Address) {
		t.Fatalf("expected existing contact %q to remain present", again.Address)
	}
}

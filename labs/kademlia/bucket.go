package kademlia

import (
	"container/list"
)

// bucket definition
// contains a List
type bucket struct {
	list *list.List
	// Replacement cache (optional): recently seen contacts that didn't fit.
	// Promoted into the bucket if a slot opens later.
	repl    []Contact
	replCap int
}

// newBucket returns a new instance of a bucket
func newBucket() *bucket {
	//bucket := &bucket{}
	//bucket.list = list.New()
	//return bucket
	b := &bucket{list: list.New(), replCap: 32}
	return b
}

// AddContact adds the Contact to the front of the bucket
// or moves it to the front of the bucket if it already existed
func (bucket *bucket) AddContact(contact Contact) {
	var element *list.Element
	for e := bucket.list.Front(); e != nil; e = e.Next() {
		nodeID := e.Value.(Contact).ID

		if (contact).ID.Equals(nodeID) {
			element = e
		}
	}

	if element == nil {
		if bucket.Len() < bucketSize {
			bucket.list.PushFront(contact)
		}
	} else {
		bucket.list.MoveToFront(element)
	}
}

// GetContactAndCalcDistance returns an array of Contacts where
// the distance has already been calculated
func (bucket *bucket) GetContactAndCalcDistance(target *KademliaID) []Contact {
	var contacts []Contact

	for elt := bucket.list.Front(); elt != nil; elt = elt.Next() {
		contact := elt.Value.(Contact)
		contact.CalcDistance(target)
		contacts = append(contacts, contact)
	}

	return contacts
}

// Len return the size of the bucket
func (bucket *bucket) Len() int {
	return bucket.list.Len()
}

// addReplacement appends to the replacement cache (bounded, no dups).
func (bucket *bucket) addReplacement(c Contact) {
	// de-dup
	for i := range bucket.repl {
		if bucket.repl[i].ID.Equals(c.ID) {
			return
		}
	}
	if len(bucket.repl) >= bucket.replCap {
		// drop oldest replacement; keep the more recent
		copy(bucket.repl, bucket.repl[1:])
		bucket.repl = bucket.repl[:bucket.replCap-1]
	}
	bucket.repl = append(bucket.repl, c)
}

// popReplacement returns the most recent replacement if any.
func (bucket *bucket) popReplacement() (Contact, bool) {
	n := len(bucket.repl)
	if n == 0 {
		return Contact{}, false
	}
	c := bucket.repl[n-1]
	bucket.repl = bucket.repl[:n-1]
	return c, true
}

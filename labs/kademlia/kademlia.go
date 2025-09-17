package kademlia

// kademlia.go: M1 node state, Join, and iterative FIND_NODE
// NOTE: variable names preserved: "routingTable" and "candidates".

import (
	"fmt"
	"sort"
	"time"
)

type Kademlia struct {
	me           Contact
	routingTable *RoutingTable
	network      *Network

	alpha      int
	timeoutRPC time.Duration
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

// Leave M2/M3 placeholders intact.
func (kademlia *Kademlia) LookupData(hash string) { /* TODO */ }
func (kademlia *Kademlia) Store(data []byte)      { /* TODO */ }

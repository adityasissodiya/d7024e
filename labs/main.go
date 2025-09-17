package main

import (
	"fmt"
	"time"

	"d7024e/kademlia"
)

func main() {
	// Two nodes
	a := kademlia.NewContact(kademlia.NewKademliaID("00112233445566778899aabbccddeeff00112233"), "127.0.0.1:9001")
	b := kademlia.NewContact(kademlia.NewKademliaID("8899aabbccddeeff00112233445566778899aabb"), "127.0.0.1:9002")

	anode, _ := kademlia.NewKademlia(a, "127.0.0.1", 9001)
	bnode, _ := kademlia.NewKademlia(b, "127.0.0.1", 9002)
	defer anode.Close()
	defer bnode.Close()

	// Join via bootstrap (PING + iterative FIND_NODE on self)
	_ = anode.Join(&b)

	// Give the async responses a moment (UDP over localhost is fast, but be safe)
	time.Sleep(300 * time.Millisecond)

	// Now a can lookup b (or any target ID)
	target := kademlia.NewContact(b.ID, "")
	anode.LookupContact(&target)

	closest := anode.ClosestContacts(b.ID, 5)
	fmt.Println("closest to B from A:", closest)
}

package kademlia

import (
	"encoding/hex"
	"math/rand"
)

// static number of bytes in a KademliaID
const IDLength = 20

// 160-bit ID
type KademliaID [IDLength]byte

// NewKademliaID returns a new ID from a hex string (40 chars)
func NewKademliaID(data string) *KademliaID {
	decoded, _ := hex.DecodeString(data)
	id := KademliaID{}
	copy(id[:], decoded)
	return &id
}

// NewRandomKademliaID returns a random ID (non-crypto)
func NewRandomKademliaID() *KademliaID {
	id := KademliaID{}
	for i := 0; i < IDLength; i++ {
		id[i] = uint8(rand.Intn(256))
	}
	return &id
}

// Less compares lexicographically (used for distance ordering)
func (kademliaID *KademliaID) Less(other *KademliaID) bool {
	for i := 0; i < IDLength; i++ {
		if kademliaID[i] < other[i] {
			return true
		}
		if kademliaID[i] > other[i] {
			return false
		}
	}
	return false
}

// Equals checks equality
func (kademliaID *KademliaID) Equals(other *KademliaID) bool {
	for i := 0; i < IDLength; i++ {
		if kademliaID[i] != other[i] {
			return false
		}
	}
	return true
}

// CalcDistance = XOR
func (kademliaID KademliaID) CalcDistance(target *KademliaID) *KademliaID {
	result := KademliaID{}
	for i := 0; i < IDLength; i++ {
		result[i] = kademliaID[i] ^ target[i]
	}
	return &result
}

// String hex-encodes the ID
func (kademliaID *KademliaID) String() string {
	return hex.EncodeToString(kademliaID[0:IDLength])
}

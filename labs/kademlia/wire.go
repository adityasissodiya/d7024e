package kademlia

// wire.go: wire protocol definitions for M1 (PING, FIND_NODE)

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Message types (M1 only)
type msgType string

const (
	msgPing       msgType = "PING"
	msgPong       msgType = "PONG"
	msgFindNode   msgType = "FIND_NODE"
	msgFindNodeOK msgType = "FIND_NODE_OK"
)

// Minimal serializable contact for the wire. We do NOT serialize the in-memory
// distance field; this avoids changing your Contact struct.
type wireContact struct {
	IDHex   string `json:"id"`
	Address string `json:"address"`
}

func (w wireContact) toContact() (Contact, error) {
	idBytes, err := hex.DecodeString(w.IDHex)
	if err != nil {
		return Contact{}, err
	}
	if len(idBytes) != IDLength {
		return Contact{}, fmt.Errorf("invalid id length: got %d want %d", len(idBytes), IDLength)
	}
	var id KademliaID
	copy(id[:], idBytes)
	return Contact{ID: &id, Address: w.Address}, nil
}

func fromContact(c Contact) wireContact {
	return wireContact{
		IDHex:   c.ID.String(),
		Address: c.Address,
	}
}

// Common envelope for all M1 messages.
type envelope struct {
	Type     msgType       `json:"type"`
	From     wireContact   `json:"from"`
	MsgID    string        `json:"msg_id"`
	TargetID string        `json:"target_id,omitempty"` // hex string
	Contacts []wireContact `json:"contacts,omitempty"`  // for FIND_NODE_OK
}

func (e envelope) marshal() ([]byte, error)  { return json.Marshal(e) }
func (e *envelope) unmarshal(b []byte) error { return json.Unmarshal(b, e) }

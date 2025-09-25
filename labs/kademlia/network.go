package kademlia

// network.go: UDP transport + M1 handlers (PING, FIND_NODE)

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"
)

// Network provides UDP-based request/response for PING and FIND_NODE.
type Network struct {
	conn        *net.UDPConn
	kademlia    *Kademlia
	mu          sync.Mutex
	inflight    map[string]chan envelope // msgID -> response chan
	readStopped chan struct{}
}

// NewNetwork binds ip:port and starts the read loop.
// NOTE: We retain your existing Listen() symbol below, but you don't need it.
// Use NewKademlia(...) which creates a Network per node.
func NewNetwork(k *Kademlia, ip string, port int) (*Network, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	n := &Network{
		conn:        conn,
		kademlia:    k,
		inflight:    make(map[string]chan envelope),
		readStopped: make(chan struct{}),
	}
	go n.readLoop()
	return n, nil
}

// Kept for compatibility with your skeleton; unused in the flow below.
func Listen(ip string, port int) { /* no-op; call NewKademlia instead */ }

func (network *Network) Close() error {
	if network.conn != nil {
		_ = network.conn.Close()
	}
	select {
	case <-network.readStopped:
	case <-time.After(200 * time.Millisecond):
	}
	return nil
}

func (network *Network) nextMsgID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (network *Network) send(to *net.UDPAddr, env envelope) error {
	b, err := env.marshal()
	if err != nil {
		return err
	}
	// Wire-level send—pairs with your REPLICATE logs.
	fmt.Printf("[NET] => %s msg=%s to=%s\n", env.Type, env.MsgID, to.String())
	_, err = network.conn.WriteToUDP(b, to)
	return err
}

func (network *Network) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, src, err := network.conn.ReadFromUDP(buf)
		if err != nil {
			close(network.readStopped)
			return
		}
		var env envelope
		if err := env.unmarshal(buf[:n]); err != nil {
			continue
		}

		fmt.Printf("[NET] <= %s msg=%s from=%s\n", env.Type, env.MsgID, env.From.Address)

		// Response path: deliver to waiter
		//
		// IMPORTANT:
		// - sendStoreTo waits for STORE_OK
		// - sendFindValueTo waits for FIND_VALUE_OK (with either Value or Contacts)
		// If we don't forward these, callers will time out spuriously.
		if env.Type == msgPong || env.Type == msgFindNodeOK ||
			env.Type == msgFindValueOK || env.Type == msgStoreOK {
			network.mu.Lock()
			ch := network.inflight[env.MsgID]
			network.mu.Unlock()
			if ch != nil {
				select {
				case ch <- env:
				default:
				}
				continue
			}
		}

		// Request path: dispatch to handlers
		switch env.Type {
		case msgPing:
			network.handlePing(env, src)
		case msgFindNode:
			network.handleFindNode(env, src)
		case msgStore:
			network.handleStore(env, src)
		case msgFindValue:
			network.handleFindValue(env, src)
		default:
			// ignore unknown types
		}
	}
}

// PING handler -> PONG
func (network *Network) handlePing(env envelope, src *net.UDPAddr) {
	// Learn/refresh sender in our routing table
	if contact, err := env.From.toContact(); err == nil &&
		network.kademlia != nil && network.kademlia.routingTable != nil {
		network.kademlia.routingTable.AddContact(contact)
	}

	// Reply
	reply := envelope{
		Type:  msgPong,
		From:  fromContact(network.kademlia.me),
		MsgID: env.MsgID, // echo the request ID back
	}
	_ = network.send(src, reply)
	fmt.Printf("[PING] from=%s -> PONG\n", env.From.Address)
}

// FIND_NODE handler -> FIND_NODE_OK
func (network *Network) handleFindNode(env envelope, src *net.UDPAddr) {
	if network.kademlia == nil || network.kademlia.routingTable == nil {
		return
	}
	idBytes, err := hex.DecodeString(env.TargetID)
	if err != nil || len(idBytes) != IDLength {
		return
	}
	var target KademliaID
	copy(target[:], idBytes)

	contacts := network.kademlia.routingTable.FindClosestContacts(&target, bucketSize)

	reply := envelope{
		Type:  msgFindNodeOK,
		From:  fromContact(network.kademlia.me),
		MsgID: env.MsgID,
	}
	reply.Contacts = make([]wireContact, 0, len(contacts))
	for _, c := range contacts {
		reply.Contacts = append(reply.Contacts, fromContact(c))
	}
	_ = network.send(src, reply)
	fmt.Printf("[FIND_NODE] from=%s target=%s returning %d contacts\n", env.From.Address, env.TargetID, len(contacts))
}

// -------- Public methods kept from your skeleton --------

// SendPingMessage sends a PING to the given peer and waits for PONG.
func (network *Network) SendPingMessage(contact *Contact) {
	fmt.Printf("[PING=>] to=%s\n", contact.Address)
	if contact == nil || contact.Address == "" {
		return
	}
	dst, err := net.ResolveUDPAddr("udp", contact.Address)
	if err != nil {
		return
	}
	env := envelope{
		Type:  msgPing,
		From:  fromContact(network.kademlia.me),
		MsgID: network.nextMsgID(),
	}
	ch := make(chan envelope, 1)
	network.mu.Lock()
	network.inflight[env.MsgID] = ch
	network.mu.Unlock()
	defer func() {
		network.mu.Lock()
		delete(network.inflight, env.MsgID)
		network.mu.Unlock()
	}()

	_ = network.send(dst, env)

	// Update our routing table only on success
	select {
	case <-ch:
		if network.kademlia != nil && network.kademlia.routingTable != nil {
			network.kademlia.routingTable.AddContact(*contact)
		}
	case <-time.After(800 * time.Millisecond):
		// timeout: treat as failure, do nothing
	}
}

// PingWait sends a PING and returns true iff we got a PONG before timeout.
// NOTE: Unlike SendPingMessage, callers expect a boolean and we avoid side-effects
// beyond the usual routingTable refresh on success.
func (network *Network) PingWait(contact *Contact, timeout time.Duration) bool {
	if contact == nil || contact.Address == "" {
		return false
	}
	dst, err := net.ResolveUDPAddr("udp", contact.Address)
	if err != nil {
		return false
	}
	env := envelope{
		Type:  msgPing,
		From:  fromContact(network.kademlia.me),
		MsgID: network.nextMsgID(),
	}
	ch := make(chan envelope, 1)
	network.mu.Lock()
	network.inflight[env.MsgID] = ch
	network.mu.Unlock()
	defer func() {
		network.mu.Lock()
		delete(network.inflight, env.MsgID)
		network.mu.Unlock()
	}()
	if err := network.send(dst, env); err != nil {
		return false
	}
	select {
	case <-ch:
		// handlePing already refreshed the sender in our table; also keep the callee.
		if network.kademlia != nil && network.kademlia.routingTable != nil {
			network.kademlia.routingTable.AddContact(*contact)
		}
		return true
	case <-time.After(timeout):
		return false
	}
}

// SendFindContactMessage asks the *peer* "contact" for nodes close to *contact.ID*.
// (Good for simple refresh. For iterative lookup with an arbitrary target, we add
// a more explicit helper below.)
func (network *Network) SendFindContactMessage(contact *Contact) {
	if contact == nil {
		return
	}
	_, _ = network.SendFindContactMessageTo(contact, contact)
}

// Explicit helper used by LookupContact: ask "peer" for nodes close to "target".
func (network *Network) SendFindContactMessageTo(peer *Contact, target *Contact) ([]Contact, error) {
	fmt.Printf("[FIND_NODE=>] peer=%s target=%s\n", peer.Address, target.ID.String())
	if peer == nil || peer.Address == "" || target == nil || target.ID == nil {
		return nil, fmt.Errorf("bad args")
	}
	dst, err := net.ResolveUDPAddr("udp", peer.Address)
	if err != nil {
		return nil, err
	}
	env := envelope{
		Type:     msgFindNode,
		From:     fromContact(network.kademlia.me),
		MsgID:    network.nextMsgID(),
		TargetID: target.ID.String(),
	}
	ch := make(chan envelope, 1)
	network.mu.Lock()
	network.inflight[env.MsgID] = ch
	network.mu.Unlock()
	defer func() {
		network.mu.Lock()
		delete(network.inflight, env.MsgID)
		network.mu.Unlock()
	}()

	if err := network.send(dst, env); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Type != msgFindNodeOK {
			return nil, fmt.Errorf("unexpected resp: %s", resp.Type)
		}
		contacts := make([]Contact, 0, len(resp.Contacts))
		for _, wc := range resp.Contacts {
			c, err := wc.toContact()
			if err == nil {
				contacts = append(contacts, c)
				// Learn discovered contacts
				if network.kademlia != nil && network.kademlia.routingTable != nil {
					network.kademlia.routingTable.AddContact(c)
				}
			}
		}
		// Learn the responder
		if c, err := resp.From.toContact(); err == nil &&
			network.kademlia != nil && network.kademlia.routingTable != nil {
			network.kademlia.routingTable.AddContact(c)
		}
		return contacts, nil

	case <-time.After(800 * time.Millisecond):
		return nil, context.DeadlineExceeded
	}
}

// ---------- M2 handlers ----------

func (network *Network) handleStore(env envelope, src *net.UDPAddr) {
	// learn sender
	if c, err := env.From.toContact(); err == nil && network.kademlia != nil && network.kademlia.routingTable != nil {
		network.kademlia.routingTable.AddContact(c)
	}
	// store locally
	if env.KeyHex != "" && len(env.Value) > 0 && network.kademlia != nil {
		network.kademlia.storeLocal(env.KeyHex, env.Value)
	}
	// ack
	_ = network.send(src, envelope{
		Type:  msgStoreOK,
		From:  fromContact(network.kademlia.me),
		MsgID: env.MsgID,
	})
	fmt.Printf("[STORE] from=%s key=%s saved=true\n", env.From.Address, env.KeyHex)
}

func (network *Network) handleFindValue(env envelope, src *net.UDPAddr) {
	if network.kademlia == nil || network.kademlia.routingTable == nil {
		return
	}
	// If we have the value locally, return it.
	if val, ok := network.kademlia.loadLocal(env.KeyHex); ok {
		_ = network.send(src, envelope{
			Type:   msgFindValueOK,
			From:   fromContact(network.kademlia.me),
			MsgID:  env.MsgID,
			KeyHex: env.KeyHex,
			Value:  val,
		})
		fmt.Printf("[FIND_VALUE] HIT key=%s from=%s returning VALUE\n", env.KeyHex, env.From.Address)
		return
	}

	// Otherwise return closest contacts to the key (treat key as ID)
	if len(env.KeyHex) == 40 {
		b, _ := hex.DecodeString(env.KeyHex)
		var target KademliaID
		copy(target[:], b)
		contacts := network.kademlia.routingTable.FindClosestContacts(&target, bucketSize)
		out := make([]wireContact, 0, len(contacts))
		for _, c := range contacts {
			out = append(out, fromContact(c))
		}
		_ = network.send(src, envelope{
			Type:     msgFindValueOK,
			From:     fromContact(network.kademlia.me),
			MsgID:    env.MsgID,
			KeyHex:   env.KeyHex,
			Contacts: out,
		})
	}

}

// ---------- M2 client helpers (internal) ----------

func (network *Network) sendStoreTo(peer *Contact, keyHex string, value []byte, timeout time.Duration) error {
	fmt.Printf("[STORE=>] to=%s key=%s\n", peer.Address, keyHex)
	if peer == nil || peer.Address == "" {
		return fmt.Errorf("bad peer")
	}
	dst, err := net.ResolveUDPAddr("udp", peer.Address)
	if err != nil {
		return err
	}
	env := envelope{
		Type:   msgStore,
		From:   fromContact(network.kademlia.me),
		MsgID:  network.nextMsgID(),
		KeyHex: keyHex,
		Value:  value,
	}
	ch := make(chan envelope, 1)
	network.mu.Lock()
	network.inflight[env.MsgID] = ch
	network.mu.Unlock()
	defer func() { network.mu.Lock(); delete(network.inflight, env.MsgID); network.mu.Unlock() }()

	if err := network.send(dst, env); err != nil {
		return err
	}
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

func (network *Network) sendFindValueTo(peer *Contact, keyHex string, timeout time.Duration) (val []byte, contacts []Contact, err error) {
	fmt.Printf("[FIND_VALUE=>] to=%s key=%s\n", peer.Address, keyHex)
	if peer == nil || peer.Address == "" {
		return nil, nil, fmt.Errorf("bad peer")
	}
	dst, err := net.ResolveUDPAddr("udp", peer.Address)
	if err != nil {
		return nil, nil, err
	}
	env := envelope{
		Type:   msgFindValue,
		From:   fromContact(network.kademlia.me),
		MsgID:  network.nextMsgID(),
		KeyHex: keyHex,
	}
	ch := make(chan envelope, 1)
	network.mu.Lock()
	network.inflight[env.MsgID] = ch
	network.mu.Unlock()
	defer func() { network.mu.Lock(); delete(network.inflight, env.MsgID); network.mu.Unlock() }()

	if err := network.send(dst, env); err != nil {
		return nil, nil, err
	}
	select {
	case resp := <-ch:
		// Learn responder + contacts we got back
		if c, err2 := resp.From.toContact(); err2 == nil && network.kademlia != nil && network.kademlia.routingTable != nil {
			network.kademlia.routingTable.AddContact(c)
		}
		for _, wc := range resp.Contacts {
			if c, err2 := wc.toContact(); err2 == nil && network.kademlia != nil && network.kademlia.routingTable != nil {
				network.kademlia.routingTable.AddContact(c)
			}
		}
		if len(resp.Value) > 0 {
			return resp.Value, nil, nil
		}
		out := make([]Contact, 0, len(resp.Contacts))
		for _, wc := range resp.Contacts {
			if c, err2 := wc.toContact(); err2 == nil {
				out = append(out, c)
			}
		}
		return nil, out, nil
	case <-time.After(timeout):
		return nil, nil, context.DeadlineExceeded
	}
}

// Kept as stubs for M2/M3.
func (network *Network) SendFindDataMessage(hash string) {}
func (network *Network) SendStoreMessage(data []byte)    {}

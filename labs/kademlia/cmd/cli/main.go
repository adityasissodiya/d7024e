package main

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"d7024e/kademlia"
)

// randomID makes a random 160-bit KademliaID.
func randomID() *kademlia.KademliaID {
	var b [20]byte
	_, _ = rand.Read(b[:])
	var id kademlia.KademliaID
	copy(id[:], b[:])
	return &id
}

// parseHexID decodes a 40-hex ID string to KademliaID.
func parseHexID(h string) (*kademlia.KademliaID, error) {
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) != 20 {
		return nil, fmt.Errorf("invalid id hex")
	}
	var id kademlia.KademliaID
	copy(id[:], raw)
	return &id, nil
}

// kademliaIDBytes returns the raw 20 bytes for printing.
func kademliaIDBytes(id *kademlia.KademliaID) []byte {
	if id == nil {
		return nil
	}
	b := make([]byte, 20)
	copy(b, id[:])
	return b
}

// splitHostPort parses "host:port" into host, port(int).
func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	return host, p, nil
}

// sha1Hex is unused; handy if you want deterministic IDs later.
func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "UDP listen address for this node, e.g. 127.0.0.1:9001")
	bootstrap := flag.String("bootstrap", "", "optional bootstrap <host:port> to join")
	idhex := flag.String("id", "", "optional 40-hex node ID (default: random)")
	flag.Parse()

	// --- Build our identity (Contact) ---
	var id *kademlia.KademliaID
	var err error
	if s := strings.TrimSpace(*idhex); s != "" {
		id, err = parseHexID(s)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERR:", err)
			os.Exit(2)
		}
	} else {
		id = randomID()
	}
	me := kademlia.NewContact(id, *addr) // Keep your Contact constructor/signature.

	// --- Parse listen address into ip + port for constructors ---
	ip, port, err := splitHostPort(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR parsing -addr:", err)
		os.Exit(2)
	}

	// --- Bring up Kademlia (your NewKademlia binds ip:port and starts network) ---
	k, err := kademlia.NewKademlia(me, ip, port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR starting node:", err)
		os.Exit(2)
	}

	// --- Optionally join a bootstrap peer ---
	if s := strings.TrimSpace(*bootstrap); s != "" && s != *addr {
		boot := kademlia.NewContact(randomID(), s) // ID will be learned on ping.
		// Small delay helps on localhost to ensure sockets are up before join.
		time.Sleep(150 * time.Millisecond)
		if err := k.Join(&boot); err != nil {
			fmt.Fprintln(os.Stderr, "WARN: join failed:", err)
		}
	}

	// --- CLI REPL ---
	quit := make(chan struct{}, 1)
	cli := kademlia.NewCLI(k, os.Stdin, os.Stdout, func() { quit <- struct{}{} })

	fmt.Printf("node up: id=%s addr=%s\n", hex.EncodeToString(kademliaIDBytes(id)), me.Address)
	if *bootstrap != "" {
		fmt.Printf("bootstrapped to %s\n", *bootstrap)
	}
	fmt.Println("commands: put <text> | get <40-hex-key> | exit")

	if err := cli.Run(); err != nil && err.Error() != "EOF" {
		fmt.Fprintln(os.Stderr, "ERR:", err)
	}
	<-quit
}

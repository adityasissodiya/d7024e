package kademlia

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"
)

// ---------------------- M3 CLI TESTS ----------------------
//
// These tests define the external behavior for a simple CLI sitting on top
// of our Kademlia node, *without* requiring an interactive TTY.
// We expect a type:
//
//   type CLI struct { /* wraps a *Kademlia and I/O */ }
//
// with a constructor:
//
//   func NewCLI(k *Kademlia, in io.Reader, out io.Writer, quit func()) *CLI
//
// and a single-step executor:
//
//   func (cli *CLI) RunLine(line string) error
//
// Contract (enforced below):
// - "put <content>": prints ONLY the 40-hex key (sha1(content)) and stores locally.
// - "get <hexkey>": prints the content and the node address it was retrieved from.
// - "exit": calls the provided quit() and returns io.EOF.
// - Errors: print a line containing "ERR" (or "NOTFOUND" for misses) and return non-nil.
//
// NOTE: We reuse M2 helpers (m2NewNode/m2StartCluster) to spin up a small network.
//

// newCLI creates a CLI bound to given node with in/out buffers and a quit channel spy.
func newCLI(k *Kademlia) (*CLI, *bytes.Buffer, *bytes.Buffer, chan struct{}) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	quitCh := make(chan struct{}, 1)
	cli := NewCLI(k, in, out, func() { quitCh <- struct{}{} })
	return cli, in, out, quitCh
}

// Test that `put <content>` prints the sha1 hex key and stores locally at origin.
func TestM3_Put_PrintsKeyAndStoresLocal(t *testing.T) {
	k, _ := m2NewNode(t)
	cli, _, out, _ := newCLI(k)

	content := "hello world"
	if err := cli.RunLine("put " + content); err != nil {
		t.Fatalf("put errored: %v", err)
	}

	// Expect key hex on its own line
	got := strings.TrimSpace(out.String())
	if len(got) != 40 {
		t.Fatalf("expected 40-char sha1 hex key, got %q (len=%d)", got, len(got))
	}
	// Validate it matches sha1(content)
	want := sha1.Sum([]byte(content))
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("key mismatch: got %s want %s", got, hex.EncodeToString(want[:]))
	}
	// Ensure value is present locally (origin stores immediately)
	if v, ok := k.loadLocal(got); !ok || string(v) != content {
		t.Fatalf("value not stored locally by Put; ok=%v v=%q", ok, string(v))
	}
}

// Test that a Get via CLI prints content AND the address it came from.
func TestM3_Get_ReturnsValueAndFromAddress(t *testing.T) {
	nodes, contacts := m2Cluster(t, 4)
	defer func() {
		for _, n := range nodes {
			n.Close()
		}
	}()

	// Put on node 0 via CLI
	cli0, _, out0, _ := newCLI(nodes[0])
	content := "lorem ipsum dolor"
	if err := cli0.RunLine("put " + content); err != nil {
		t.Fatalf("put errored: %v", err)
	}
	key := strings.TrimSpace(out0.String())
	if len(key) != 40 {
		t.Fatalf("bad key: %q", key)
	}

	// Now Get from node 1 via CLI
	cli1, _, out1, _ := newCLI(nodes[1])
	if err := cli1.RunLine("get " + key); err != nil {
		t.Fatalf("get errored: %v", err)
	}
	out := out1.String()
	if !strings.Contains(out, content) {
		t.Fatalf("get output didn't include content; out=%q", out)
	}
	// Should include a "from" address that belongs to the cluster.
	var foundAddr bool
	for _, c := range contacts {
		if strings.Contains(out, c.Address) {
			foundAddr = true
			break
		}
	}
	if !foundAddr {
		t.Fatalf("get output didn't include a known node address; out=%q", out)
	}
}

// Test that exit calls quit and returns io.EOF so a REPL can stop.
func TestM3_Exit_CallsQuit(t *testing.T) {
	k, _ := m2NewNode(t)
	cli, _, _, quitCh := newCLI(k)

	if err := cli.RunLine("exit"); err != io.EOF {
		t.Fatalf("exit should return io.EOF, got %v", err)
	}
	select {
	case <-quitCh:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("quit func not called")
	}
}

// Test validations: missing args, invalid hex, not found.
func TestM3_Put_RequiresArgument(t *testing.T) {
	k, _ := m2NewNode(t)
	cli, _, out, _ := newCLI(k)

	if err := cli.RunLine("put"); err == nil {
		t.Fatalf("expected error for missing argument")
	}
	if !strings.Contains(out.String(), "ERR") {
		t.Fatalf("expected ERR message, got %q", out.String())
	}
}

func TestM3_Get_RequiresValidHex(t *testing.T) {
	k, _ := m2NewNode(t)
	cli, _, out, _ := newCLI(k)

	err := cli.RunLine("get not-a-hex")
	if err == nil {
		t.Fatalf("expected error for invalid hex key")
	}
	s := out.String()
	if !strings.Contains(s, "ERR") {
		t.Fatalf("expected ERR message, got %q", s)
	}
}

func TestM3_Get_NotFound(t *testing.T) {
	nodes, _ := m2Cluster(t, 3)
	defer func() {
		for _, n := range nodes {
			n.Close()
		}
	}()

	cli, _, out, _ := newCLI(nodes[0])
	// random-looking 20-byte key hex (40 chars)
	key := "00112233445566778899aabbccddeeff00112233"

	err := cli.RunLine("get " + key)
	if err == nil {
		t.Fatalf("expected not found error")
	}
	if !strings.Contains(out.String(), "NOTFOUND") && !strings.Contains(out.String(), "ERR") {
		t.Fatalf("expected not found/ERR message, got %q", out.String())
	}
}

// Unknown command should print an error.
func TestM3_UnknownCommand_ShowsError(t *testing.T) {
	k, _ := m2NewNode(t)
	cli, _, out, _ := newCLI(k)

	if err := cli.RunLine("frobnicate 123"); err == nil {
		t.Fatalf("expected error for unknown command")
	}
	if s := out.String(); !strings.Contains(s, "ERR") && !strings.Contains(strings.ToLower(s), "unknown") {
		t.Fatalf("expected unknown/ERR message, got %q", s)
	}
}

// Whitespace handling: extra spaces between tokens should be tolerated.
func TestM3_Whitespace_Tolerated(t *testing.T) {
	k, _ := m2NewNode(t)
	cli, _, out, _ := newCLI(k)

	content := "spaced content"
	if err := cli.RunLine("put    " + content); err != nil {
		t.Fatalf("put with extra spaces errored: %v", err)
	}
	key := strings.TrimSpace(out.String())
	out.Reset()

	if err := cli.RunLine("get    " + key); err != nil {
		t.Fatalf("get with extra spaces errored: %v", err)
	}
	if !strings.Contains(out.String(), content) {
		t.Fatalf("get output didn't include content; out=%q", out.String())
	}
}

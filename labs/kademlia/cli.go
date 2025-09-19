package kademlia

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CLI is a thin command layer over a running Kademlia node.
// It does not own the node's lifecycle; it only issues commands to it.
type CLI struct {
	k    *Kademlia
	in   io.Reader
	out  io.Writer
	quit func()
}

// NewCLI constructs a CLI over the provided node.
// `in` and `out` are the I/O streams; `quit` is invoked on "exit".
func NewCLI(k *Kademlia, in io.Reader, out io.Writer, quit func()) *CLI {
	if quit == nil {
		quit = func() {}
	}
	return &CLI{k: k, in: in, out: out, quit: quit}
}

// RunLine executes a single command line.
// Expected commands:
//
//	put <content>      -> prints 40-char sha1 hex
//	get <key-hex>      -> prints the content and a "from <addr>" line
//	exit               -> calls quit() and returns io.EOF
//
// On error, it prints a line containing "ERR" (or "NOTFOUND" for misses)
// and returns a non-nil error.
func (cli *CLI) RunLine(line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	cmd, arg := splitOnce(line)

	switch strings.ToLower(cmd) {
	case "put":
		content := strings.TrimSpace(arg)
		if content == "" {
			fmt.Fprintln(cli.out, "ERR missing argument")
			return errors.New("put: missing argument")
		}
		keyHex, err := cli.k.Put([]byte(content))
		if err != nil {
			fmt.Fprintf(cli.out, "ERR %v\n", err)
			return err
		}
		// Print ONLY the key (tests expect a clean 40-hex line)
		fmt.Fprintln(cli.out, keyHex)
		return nil

	case "get":
		keyHex := strings.TrimSpace(arg)
		if keyHex == "" {
			fmt.Fprintln(cli.out, "ERR missing argument")
			return errors.New("get: missing argument")
		}
		// Basic validation: 20-byte hex (40 chars) and valid hex digits
		if len(keyHex) != 40 || !isValidHex(keyHex) {
			fmt.Fprintln(cli.out, "ERR invalid key")
			return errors.New("get: invalid key")
		}
		val, from, err := cli.k.Get(keyHex)
		if err != nil || val == nil {
			fmt.Fprintln(cli.out, "NOTFOUND")
			if err == nil {
				err = errors.New("not found")
			}
			return err
		}
		// Print content and from-address (tests look for substrings only)
		fmt.Fprintf(cli.out, "%s\nfrom %s\n", string(val), from.Address)
		return nil

	case "exit":
		cli.quit()
		return io.EOF

	default:
		fmt.Fprintln(cli.out, "ERR unknown command")
		return errors.New("unknown command")
	}
}

// Run starts a simple REPL on cli.in until EOF or "exit".
//
// Not required by tests, but useful for manual runs.
// It ignores blank lines and prints minimal errors (consistent with RunLine).
func (cli *CLI) Run() error {
	sc := bufio.NewScanner(cli.in)
	for sc.Scan() {
		if err := cli.RunLine(sc.Text()); err == io.EOF {
			return nil
		}
	}
	return sc.Err()
}

// --- helpers ---

// splitOnce splits on the first span of whitespace into (head, tail).
// If no whitespace, tail is "".
func splitOnce(s string) (head, tail string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	// find first whitespace boundary
	i := -1
	for idx, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			i = idx
			break
		}
	}
	if i < 0 {
		return s, ""
	}
	// skip subsequent spaces to preserve the original arg text as much as practical
	j := i + 1
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	return s[:i], s[j:]
}

func isValidHex(h string) bool {
	_, err := hex.DecodeString(h)
	return err == nil
}

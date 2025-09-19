// Package kademlia implements the parts of Kademlia required by the D7024E lab.
//
// What’s here (M1, M2, M3 in one place)
// -------------------------------------
// M1: Network formation
//   - PING/PONG over UDP with request/response book-keeping.
//     Nodes learn/refresh the sender in their routingTable on PING.
//   - Join(bootstrap): PING bootstrap, then iterative FIND_NODE on our own ID
//     to seed routingTable.
//   - Iterative node lookup (LookupContact) using α parallel queries.
//     Variable names from the skeleton are preserved: routingTable, candidates.
//
// M2: Object distribution (values)
//   - Put(data): compute SHA-1 key; store locally immediately; replicate to the
//     K closest nodes to the key (K = bucketSize). Transport uses STORE / STORE_OK.
//   - Get(keyHex): check local store; otherwise iterative FIND_VALUE with early
//     exit on first value. On success, cache value locally.
//
// M3: Minimal CLI (cmd/cli)
//   - put <bytes>       -> prints content hash (40-hex SHA-1).
//   - get <40-hex-hash> -> prints value and the address it came from.
//   - exit              -> terminates the node.
//   - Flags:
//     --addr       <ip:port>   (required)
//     --bootstrap  <ip:port>   (optional; causes Join to that node)
//
// Repository layout (relevant bits)
// ---------------------------------
//
//	kademlia/                 This package
//	  kademlia.go             Node state + Join + LookupContact + Put/Get
//	  network.go              UDP transport + PING/FIND_NODE/STORE/FIND_VALUE
//	  wire.go                 On-wire message types & (un)marshaling
//	  bucket.go               LRU buckets
//	  routingtable.go         Routing table, FindClosestContacts
//	  kademliaid.go           ID type & XOR distance
//	  cmd/cli/                Small interactive CLI (M3)
//	  m1_network_test.go      M1 tests (ping, join, lookup)
//	  m2_value_test.go        M2 tests (put/get, replication, caching, edges)
//
// Quick start for examiners
// -------------------------
// 1) Run all tests with coverage:
//
//	# From repo root or from labs/kademlia
//	go test ./... -covermode=atomic -coverpkg=./... -coverprofile=coverage.out
//	go tool cover -func=coverage.out
//	# Optional HTML report:
//	go tool cover -html=coverage.out -o coverage.html
//
// 2) Bring up two nodes and exercise M1 + M2 via CLI:
//
//	Terminal A:
//	  cd labs/kademlia/cmd/cli
//	  go run . --addr 127.0.0.1:9001
//
//	Terminal B (bootstraps to A):
//	  cd labs/kademlia/cmd/cli
//	  go run . --addr 127.0.0.1:9002 --bootstrap 127.0.0.1:9001
//
//	In Terminal B:
//	  put hello
//	  # -> prints a 40-hex key (SHA-1 of "hello")
//
//	In Terminal A (or B):
//	  get <that-40-hex-key>
//	  # -> prints: value=<bytes> from=127.0.0.1:<port>
//
//	Type 'exit' in each terminal to quit.
//
// 3) One-shot, single test under the debugger (Delve):
//
//	# Build an unoptimized test binary (Windows PowerShell shown):
//	cd labs/kademlia
//	go test -c -gcflags=all="-N -l" -o m1m2.test.exe
//
//	# Run a focused test with flags passed through (note the --%% trick in PS):
//	dlv exec .\m1m2.test.exe --% -- -test.run=TestM2_PutAndGet_SucceedsAcrossNetwork -test.v -test.count=1 -test.timeout=60s
//
//	# In Delve, handy breakpoints/tracepoints:
//	(dlv) b (*Kademlia).Put
//	(dlv) b (*Kademlia).Get
//	(dlv) trace (*Network).sendStoreTo
//	(dlv) trace (*Network).handleStore
//	(dlv) trace (*Network).sendFindValueTo
//	(dlv) trace (*Network).handleFindValue
//	(dlv) c
//
// Design notes tied to code
// -------------------------
//   - NewKademlia(Contact, ip, port) wires a node to a UDP socket and starts the
//     read loop. routingTable is created up front. Network is per node.
//   - Join(bootstrap) performs the canonical Kademlia join: PING the bootstrap,
//     then iterate FIND_NODE on our own ID to populate routingTable.
//   - LookupContact runs an α-parallel iterative search. It repeatedly pulls the
//     next α unvisited contacts closest to the target from routingTable,
//     dispatches FIND_NODE, learns returned contacts into routingTable, and
//     stops on convergence (best distance no longer improves).
//   - Put/Get: Put stores locally first (avoids a read-after-write miss), then
//     replicates to the K closest nodes. Get checks local, then runs iterative
//     FIND_VALUE; first value wins, and we cache it locally.
//
// Cross-platform notes
// --------------------
//   - On Windows PowerShell, when passing -test.* flags to a test binary under
//     Delve, use `--%` to stop PS from parsing them:
//     dlv exec .\m1m2.test.exe --% -- -test.run=... -test.timeout=60s
//
// • You don’t need CGO; the transport is pure UDP.
//
// That’s it: tests prove M1/M2 behavior, the CLI lets you poke it live, and the
// doc you’re reading is kept with the package to make the examination easy.
package kademlia

# D7024E Kademlia

This project is a **distributed key-value store**. You give it some text, it **hashes** it (SHA-1) to a 40-hex “key” and stores the text across the network. Later, anyone with the key can fetch the text. There’s no central server; **many small programs (nodes)** cooperate to store and find stuff. The algorithm they use to find each other fast is **Kademlia**.

## Architecture Diagram

![Kademlia Architecture](labs/docs/kademlia_architecture.svg)

**Mapping work to the rubric:**

* **M1 (Network formation):** PING, join, and node lookup implemented to form/maintain the network. 
* **M2 (Object distribution):** `put` stores to K-closest; `get` retrieves via iterative FIND_VALUE with path caching and periodic republish (see §2). 
* **M3 (CLI):** `put <text>`, `get <key>`, `exit`. (Shown in demos and Docker quickstart below.) 
* **M4 (Unit tests & emulation ≥1000 nodes + drop%):** Large-scale simulator + coverage instructions (see §4–§5).  
* **M5 (Containerization ≥50 containers):** Compose-driven bring-up with one seed + N scalable nodes (see §7). 
* **M6 (Lab report):** This readme anchors the system architecture/implementation overview the report asks for. 
* **M7 (Concurrency & thread safety):** Explicit design and invariants (see §3). 

## 1) Booting a node

**Command:**

```bash
go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9001
```

### CLI spins up the engine

* **`cmd/cli/main.go`**

  * Parses flags: `-addr`, `-bootstrap` (optional), `-id` (optional).
  * Builds `me` (your **Contact**) with a 160-bit **KademliaID** and string **Address** `"127.0.0.1:9001"`.
  * Calls **`kademlia.NewKademlia(me, ip, port)`**.

### Kademlia constructs subsystems

* **`kademlia.go : NewKademlia`**

  * Creates **routing table**: `routingtable.NewRoutingTable(me)`.
  * Starts **network stack**: `network.NewNetwork(kademlia, ip, port)` → binds a UDP socket.
  * Wires LRU-eviction liveness probe: `routingTable.SetPingFunc(c → network.PingWait(&c, timeout))`.
  * Starts background **republisher goroutine** (ticker).
* **`network.go : NewNetwork`**

  * Opens `net.UDPConn` on `127.0.0.1:9001`.
  * Spawns **`readLoop()`** goroutine that continuously:

    1. `ReadFromUDP` → raw bytes
    2. **`wire.go : envelope.unmarshal`** → parse message
    3. If it’s a **response** (`PONG`, `FIND_NODE_OK`, `FIND_VALUE_OK`, `STORE_OK`) → deliver to waiting goroutine via **`inflight[msgID] <- env`**
       (this is the waiter map keyed by `MsgID`, protected by `network.mu`)
    4. If it’s a **request** (`PING`, `FIND_NODE`, `FIND_VALUE`, `STORE`) → call corresponding **handler**.

### Optional bootstrap (when `-bootstrap` is given)

```bash
go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9002 -bootstrap 127.0.0.1:9001
```
* **`network.go : SendPingMessage(bootstrap)`** to learn/refresh.
* **`kademlia.go : LookupContact(&myID)`** (or a similar warmup) so your table isn’t empty: internally issues `FIND_NODE` to neighbors and adds returned contacts.
* **`routingtable.go : AddContact`** handles insertion/eviction:

  * If bucket has room → insert MRU.
  * If full → **ping LRU** (outside the lock).

    * If **dead** → evict and insert newcomer.
    * If **alive** → keep LRU; put newcomer in **replacement cache**.

> At this point the node is “up”: UDP reader running, routing table initialized, and the CLI shows the banner.

## 2) `put <text>` — publishing data

### CLI takes your input

* **`cli.go : runREPL`** reads a line, recognizes `put <rest-of-line>`, converts to `[]byte`, calls:

  * **`kademlia.go : (*Kademlia).Put(data)`**

### Put: compute key, store locally, replicate

* **`kademlia.go : Put`**

  1. **`keyFromData`**: SHA-1 of the bytes → 20-byte key; printed as **40-hex**.
  2. **`storeLocal(keyHex, data)`**:

     * Locks `storeMu`, copies the value, writes to `valueStore[keyHex]`. (Origin always keeps a copy.)
  3. Marks this `keyHex` in `originKeys` so the **republisher** will maintain it.
  4. **`replicateToClosest(keyHex, keyID, data)`**:

     * **Refresh view** near the key: `LookupContact(&target=keyID)` (iterative `FIND_NODE`) so the table contains the right neighbors around that key.
     * **`routingtable.FindClosestContacts(keyID, K)`** → the K best peers.
     * For each peer (skip self) → **`network.sendStoreTo(peer, keyHex, data, timeout)`** (fire many in parallel or sequentially, depending on your implementation).

### What a **STORE** looks like on the wire

* **Sender path (`sendStoreTo`)**

  * Build **`wire.envelope{Type: STORE, From: me, MsgID, KeyHex, Value}`**.
  * Register a **waiter channel**: `inflight[msgID] = ch`.
  * **`send()`**: `env.marshal()` → `conn.WriteToUDP`.
  * `select` for either `STORE_OK` on `ch` or `time.After(timeout)`.
* **Receiver path (`handleStore`)**

  * **`kademlia.storeLocal(keyHex, value)`** (now the replica holds it).
  * **Reply** with **`STORE_OK`** envelope (same `MsgID`).
  * **`routingTable.AddContact(env.From)`** (we learned a sender; keep table fresh).

> **Result:** the value now lives on the K nodes **closest by XOR distance to the key**, plus the origin. The **republisher goroutine** (in `kademlia.go`) will periodically redo the *same* replication to the **current** closest—so if the network changed, the data “migrates” toward the right place.

---

## 3) `get <40-hex-key>` — retrieving data

### CLI calls Get

* **`cli.go → kademlia.go : (*Kademlia).Get(keyHex)`**

### Get: local check → iterative network search

* **`kademlia.go : Get`**

  1. **Parse** `keyHex` → `KademliaID`.
  2. **Local cache**: `loadLocal`; if hit → return instantly (and CLI prints the value + `from <addr>`).
  3. Otherwise, start **iterative lookup** toward the key:

     * Prepare a **visited set**.
     * **Pick α** unvisited contacts closest to the key from `routingtable.FindClosestContacts` → this is your **batch**.
     * For each peer in the batch, kick off a goroutine: **`network.sendFindValueTo(peer, keyHex, timeout)`**.

       * Each goroutine:

         * Builds a `FIND_VALUE` envelope, registers `inflight[msgID] = ch`, `send()`, `await ch or timeout`.
         * Returns either **`{value, from}`** or **`{contacts}`**.
     * **Collect results**:

       * If **any** returns a value:
         a) **`storeLocal(keyHex, val)`** (cache at requester),
         b) **Path-cache**: choose the best contact you queried (close to the key, not the responder/self) and **`sendStoreTo`** it,
         c) return the value + responder.
       * If none returned a value: **merge** all returned contacts into your candidate pool (dedupe, mark visited), **recompute** the next batch of α **closest unvisited** and repeat.
     * **Stop condition**: either value found, or you’ve converged (best distance didn’t improve) / no new contacts → give up (`NOTFOUND`).

### What **FIND_VALUE** looks like on the wire

* **Sender path (`sendFindValueTo`)**

  * Envelope: **`{Type: FIND_VALUE, From, MsgID, KeyHex}`**.
  * Register waiter in `inflight`.
  * `send`, await **`FIND_VALUE_OK`** or timeout.
* **Receiver path (`handleFindValue`)**

  * If **have the value** locally: reply with **`FIND_VALUE_OK{ Value: <bytes> }`**.
  * Else: reply with **`FIND_VALUE_OK{ Contacts: <closest-to-key> }`**.
  * In both cases, `routingTable.AddContact(From)` to keep knowledge fresh.

> **Result:** the query “walks” the ID space, always toward nodes **closer to the key**. The first node that has the value stops the search. The requester caches it locally and (with **path caching**) plants it on a good nearby node so the *next* search is cheaper. Meanwhile, the **republisher** ensures long-term that the K **currently** closest nodes hold the value.

## 4) The “envelope” (your packet on the wire)

Every RPC uses the same wrapper:

* **`wire.go : type envelope`**

  * `Type` (one of: `PING`, `PONG`, `FIND_NODE`, `FIND_NODE_OK`, `FIND_VALUE`, `FIND_VALUE_OK`, `STORE`, `STORE_OK`)
  * `From` (contact: `ID` + `Address`)
  * `MsgID` (random per request; used to correlate responses)
  * Optional fields depending on `Type`:

    * `TargetID` (for `FIND_NODE`)
    * `KeyHex`, `Value` (for `STORE` / `FIND_VALUE`)
    * `Contacts` (for `FIND_NODE_OK` / `FIND_VALUE_OK` when returning peer lists)
* **`marshal` / `unmarshal`** turn it into bytes and back.

**Delivery:**

* **`network.go : send()`** → `conn.WriteToUDP(b, to)`
* **`network.go : readLoop()`** → `conn.ReadFromUDP(buf)` → `unmarshal` → deliver to waiter or handler.

**Correlation:**

* Sender registers a waiter channel in **`inflight`** keyed by `MsgID`, waits with a timeout.
* **readLoop** finds **`inflight[msgID]`** and pushes the response envelope into it (non-blocking send).

## 5) Routing table dynamics (what changes during all this)

* **`routingtable.go : AddContact`**

  * Any time a message arrives from `X` or you get a list including `X`, you add/refresh `X`.
  * **Not full** → push front (MRU).
  * **Full** → fetch **LRU** at the back, **release the lock**, `PingWait(LRU)`:

    * **No PONG**: relock, remove LRU, insert newcomer (MRU).
    * **PONG**: relock, move LRU to MRU (since we just saw it), **replacement cache** gets the newcomer.
* **`bucket.go`**

  * The list that keeps LRU order + a tiny **replacement cache** for overflow contacts.

**Why this matters:** lookups depend on having **fresh, live contacts** in the right parts of the ID space. You don’t throw out reliable nodes for random new ones; you **do** replace dead ones.

## 6) Background republisher (keeps placement correct over time)

* **`kademlia.go : republisher()`** (goroutine, ticker)

  * Snapshot **origin keys** you’ve put.
  * For each: read value (under `storeMu`), **release locks**, then call **`replicateToClosest`** again.
  * That re-runs the “find current K closest & STORE” loop.
  * Newly joined closer nodes will **eventually** receive the value even if it was published long ago.

## 7) Failure modes & protections (you’ll see these in traces)

* **Timeouts everywhere**: any RPC can drop; your code moves on (α-way parallelism helps).
* **Non-blocking response delivery**: `readLoop` won’t stall if a waiter already timed out (waiter channels are size-1, send is guarded).
* **No network under locks**: eviction pings and replication happen after releasing table/store locks. Prevents deadlocks with the `readLoop`.
* **Copies on store/load**: value bytes are copied on write and on read → no sharing the same slice across goroutines.

## TL;DR journey

1. **Start** → socket bound; `readLoop` listening; routing table ready; (optional) ping/bootstrap warm-up.
2. **put** → hash → local store → find current K-closest → `STORE` to each → replicas acknowledge.
3. **get** → local check → iterative α-parallel `FIND_VALUE` walk toward the key:

   * Either a node returns the **value** → requester caches + **path-caches** on a close node → done.
   * Or nobody has it → returns `NOTFOUND`.
4. **In the background** the **republisher** periodically re-sends to the *current* closest so data migrates as the network changes.
5. **All along**, the routing table keeps itself sane: LRU eviction with liveness pings, replacement cache for newcomers.

## Debugging with Delve (Windows-proof, copy/paste)

Delve steps for M1/M2 with reliable Windows command forms, including building an unoptimized test binary and a minimal dlv command set. Includes pre-made breakpoint/trace scripts and “tour” flows (M2 happy path, concurrent GETs, M1 Ping/Lookup). 

**TL;DR:**

```powershell
# from: ...\d7024e\labs\kademlia
go test -c -gcflags=all="-N -l" -o m1m2.test.exe
dlv exec .\m1m2.test.exe --% -- -test.run=TestM2_PutAndGet_SucceedsAcrossNetwork -test.v=true -test.count=1 -test.timeout=60s
# (dlv) paste:
clearall
b (*Kademlia).Put
b (*Kademlia).Get
trace (*Network).sendStoreTo
trace (*Network).handleStore
trace (*Network).sendFindValueTo
trace (*Network).handleFindValue
config max-string-len 256
c
```

## What we do (concurrency primitives in use)

1. **Goroutines for network I/O + per-RPC fan-out**

* A dedicated **UDP read loop** runs in its own goroutine and never blocks the caller. It demultiplexes responses to the right waiter and dispatches requests to handlers. 
* Lookups (`LookupContact` / `Get`) **fan out** to multiple peers **in parallel**: each peer RPC runs in a separate goroutine, results are joined through channels. This is your α-parallelism. 

2. **Channels for request/response correlation**

* For every outbound RPC, the sender allocates a **response channel** (buffer 1), registers it in `inflight[msgID]`, and waits on it or times out. The **read loop** pushes the matching envelope into that channel. 

3. **Mutexes / RWMutexes for shared state**

* `network.mu` protects the **inflight map** and request registration/removal. 
* `routingTable.mu` is an **RWMutex** guarding buckets (reads during closest-contact queries, writes on insert/move/evict). Buckets themselves are mutated **only while the table lock is held**. 
* `storeMu` (RWMutex) protects the **value store** map; reads/writes copy byte slices to avoid aliasing across goroutines. 

4. **Timers/timeouts for liveness and to avoid leaks**

* All blocking waits on RPC responses have `time.After` timeouts. If nothing arrives, we clean up the waiter and move on—no goroutine leaks, no permanent stalls. 

5. **Background maintenance goroutines (republisher)**

* A **ticker-driven republisher** runs in its own goroutine, periodically re-replicating origin values to the current K-closest nodes. Clean shutdown via a stop channel. 

---

## Where we do it (by subsystem)

## Networking (`network.go`)

* **`readLoop()`**: runs as a goroutine; reads UDP; `unmarshal`s;

  * If it’s a **response** (PONG / FIND_NODE_OK / FIND_VALUE_OK / STORE_OK), it **signals the waiting goroutine** via `inflight[msgID] <- env` (non-blocking send to a size-1 channel so the loop never deadlocks).
  * If it’s a **request** (PING / FIND_NODE / FIND_VALUE / STORE), it calls the appropriate handler which may update routing and/or store. 

* **inflight map**:

  * On send: create `ch := make(chan envelope, 1)`, `inflight[msgID] = ch` under `network.mu`.
  * On completion: remove the entry in a `defer` to guarantee cleanup on all paths.
  * On receive: readLoop looks up `inflight[msgID]` under `network.mu` and signals it.
    This is the core request/response correlation point. 

* **Client helpers** (all run concurrently per peer):

  * `SendFindContactMessageTo` (for FIND_NODE) and `sendFindValueTo` (for FIND_VALUE) create a waiter, send the request, then `select { case resp := <-ch ...; case <-time.After(...) ... }`. This makes **each RPC fully concurrent** and failure-isolated. 

* **Close()**: closes the socket and waits for `readStopped` to trip, preventing races between shutdown and the read loop. 

**Why:**

* Single read loop = one place to touch the socket; channels + inflight map give lock-free handoff to the waiting goroutine; buffered channels + default send prevent the loop from stalling if a waiter misbehaves. Timeouts keep the system live under packet loss.

## Lookup engine (`kademlia.go`)

* **`LookupContact`** (iterative FIND_NODE) and **`Get`** (iterative FIND_VALUE) implement α-parallel querying:

  * Build the next **batch** of up to α **unvisited** closest contacts.
  * For each peer in the batch, **spawn a goroutine** issuing the RPC; push each result into a batch-scoped channel.
  * After collecting all batch results, merge contacts into the routing table (via network handlers) and check the **convergence criterion** (best distance stopped improving). Repeat until convergence or value found. 

* **Local value store** uses `storeMu` (RWMutex).

  * `storeLocal` acquires a **write** lock, lazily initializes the map, and **copies** the bytes before storing.
  * `loadLocal` acquires a **read** lock and returns a **copy** of the stored bytes. This prevents concurrent mutation bugs by callers. 

* **Republisher goroutine**:

  * Started after `NewNetwork`.
  * `ticker := time.NewTicker(republishInterval)`; on each tick, snapshot `originKeys` under `originMu`, read values under `storeMu`, then call `replicateToClosest` (networking) **outside** those locks.
  * Clean exit when the stop channel is closed in `Close()`. 

**Why:**

* α-parallelism gives you the performance Kademlia expects.
* Copy-on-store and copy-on-read make the store safe even if callers (CLI/tests) hold onto slices across goroutines.
* Republisher runs concurrently but never holds data locks while doing network I/O—this avoids lock-hold cycles.

## Routing (`routingtable.go`, `bucket.go`)

* **Routing table** has an **RWMutex**; all bucket mutations happen under **write** lock, and lookups (`FindClosestContacts`) under **read** lock. Buckets are simple `container/list` LRU lists; we never touch them without holding the table lock. 

* **LRU eviction policy with liveness ping** (your new behavior):

  * If the bucket is **full**: capture the LRU contact, **release the lock**, do a `PingWait(lru)` (network call) **outside** the lock, then re-lock and either **evict** or **keep** the LRU; insert or replacement-cache the newcomer accordingly.
  * This design strictly avoids doing network I/O while holding `routingTable.mu`. 

**Why:**

* The table is shared by: the **read loop** (handlers) and the **application goroutines** (lookups/puts). The RWMutex ensures those meet safely.
* Pinging outside the lock prevents deadlocks (e.g., the read loop needs the lock to add contacts when the PONG arrives; holding it would deadlock).

## CLI (`cli.go`)

* The CLI runs a simple scanner loop (single goroutine). All concurrency is in the node/network layers it calls into. That’s intentional—keeps the front-end dead simple. 

## How we guarantee thread safety (concrete invariants)

1. **Every shared map/list is behind a lock**

* `network.inflight` → `network.mu`; `routingTable.buckets`/`bucket.list` → `routingTable.mu`; `valueStore` → `storeMu`. No unlocked writes, period. 

2. **No network I/O while holding high-level locks**

* The routing table’s eviction path *releases* the lock before pinging LRU; republisher reads keys/values under locks, then **releases locks before** doing `STORE` RPCs. This prevents lock ordering inversions with the read loop (which itself grabs the table lock in handlers). 

3. **Response routing never blocks the read loop**

* Waiter channels are **size 1**; the read loop uses a **non-blocking send** (select with `default`) to deliver the response and then continue. Even if a caller already timed out (and no one’s listening), the loop won’t get stuck. 

4. **Timeouts everywhere a goroutine could otherwise wait forever**

* All RPC waits use `select { case resp := <-ch ...; case <-time.After(...) }` with `defer` to remove inflight entries, guaranteeing cleanup and preventing leaks. 

5. **Byte-slice copies** in the store

* We never hand out references to internal storage; both store and load copy. That prevents cross-goroutine mutation of shared buffers. 

6. **Single socket reader**

* Only the read loop reads from the UDP socket, removing the need for additional synchronization on the network read path. Send path uses the kernel for serialization; our own `send()` is stateless apart from marshaling. 

## Why this design (trade-offs & failure modes we avoid)

* **Throughput & latency:** α-parallelism + per-peer goroutines give you the expected sub-linear lookup latency while preserving correctness. If a subset of peers are slow or unresponsive, timeouts isolate them without stalling the whole lookup. 

* **Simplicity where it counts:** one read loop, small critical sections with clear ownership. Locks wrap only data structures, never the network. That makes deadlocks unlikely and debuggable if they ever appear. 

* **Correctness under churn:**

  * Routing updates from *any* message handler are safe (handlers run on the read-loop goroutine, but the table is protected by RWMutex).
  * Evictions probe liveness without risking lock deadlocks.
  * Republishing runs concurrently but respects data-structure locks and never blocks readers for long. 

* **Defensive coding:** non-blocking delivery to waiters + timeouts mean an application bug on the caller side can’t wedge the network loop. 

## If you want to validate this (quick checklist)

* Run with the race detector:
  `go test ./labs/kademlia -race` and `go run -race ./labs/kademlia/cmd/cli ...`
  You should see **no race reports** under normal put/get, bootstrapping, and eviction tests.

* Turn on your debug prints (NET/GET/REPLICATE) and hammer lookups while starting/stopping peers; the node should keep making progress, and the read loop should never stall.

---

## File anchors

* **Networking:** read loop, inflight map, RPC helpers, timeouts, Close. 
* **Lookups & Store:** α-parallel fan-out, convergence, thread-safe value store, republisher goroutine. 
* **Routing:** RWMutex on table, LRU buckets, eviction/ping outside lock, closest-contacts query under RLock. 
* **CLI:** single-threaded front-end. 
* **Wire types / message kinds:** shared envelope format used across goroutines. 

That’s the concurrency story: **concurrent message handling** with **goroutines + channels**, and **thread safety** via **scoped locks**, **timeouts**, and **no network under locks**.

## Unit testing & coverage (M4)

**Run everything from the package directory**: `…\d7024e\labs\kademlia`. Guidance includes reliable PowerShell commands for coverage, HTML reports, and race detector on Windows (with MSYS2 GCC). Also has filtered runs for only M1/M2/M3 tests. 

**Quick coverage:**

```powershell
cd C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia
go test . -count=1 -timeout=120s -cover
```

**Full profile + HTML (Windows-robust):**

```powershell
$path = "C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia\coverage.out"
cmd /c "go test . -count=1 -timeout=120s -covermode=atomic -coverpkg=./... -coverprofile=""$path"""
go tool cover -func "$path"
go tool cover -html "$path" -o "C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia\coverage.html"
```

## Large-scale emulation (M4: 1000–2000 nodes + packet drop)

A **test-only simulator** drives 1000+ nodes and applies a configurable **drop %** to deliveries—no runtime code changes. Flags: `-m4.nodes`, `-m4.drop`, `-m4.seed`. Commands for both PS and bash are provided; defaults are 1000 nodes/10% drop. 

**Examples (PowerShell):**

```powershell
cd .\labs\kademlia
# Default 1000 nodes, 10% drop
go test . -count=1 -timeout=180s -run '^TestM4_Simulation_'

# 2000 nodes, 30% drop, deterministic seed
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_' "-m4.nodes=2000" "-m4.drop=30" "-m4.seed=42"
```

Why a simulator? Because 1000+ UDP sockets on localhost is brittle; simulator is fast/deterministic and keeps production code untouched. We still exercise **K-closest replication** and **FIND_VALUE** under drop. 

We have packet drop in the **M4 simulator**. It’s implemented in the test harness (not in the real UDP stack), and it’s applied in three places:

* during **STORE** replication (put path),
* on **FIND_VALUE** request delivery (get path),
* on **FIND_VALUE** response delivery (get path).

### Where packet drop lives (and how it’s wired)

* **Flags (knobs):**
  `-m4.nodes` (default 1000), `-m4.drop` (percent 0..100, default 10), `-m4.seed` (PRNG seed). These are parsed for all M4 sim tests. 

* **Drop function:**

  ```go
  func (c *simCluster) dropped() bool { ... return p < c.dropPct }
  ```

  Uses a deterministic `math/rand` (seeded) guarded by a mutex; returns true with probability `dropPct%`. 

* **Drop applied on `put` (replication):**
  In `simPut`, after choosing the K closest nodes to the key, each **STORE** to a peer is skipped if `dropped()` returns true. Origin is always stored locally (no drop). Resulting `replicatedTo` reflects how many actually received the value. 

* **Drop applied on `get` (lookup):**
  In `simGet`, the simulator iterates the K closest nodes. For each candidate:

  * It may drop the **request** (`if c.dropped() { continue }`),
  * If the node has the value, it may still drop the **response** (`if c.dropped() { continue }`).
    Any successful request+response returns the value immediately. 

* **Tests that exercise it:**

  * **No drop** happy path over 1000 nodes: all sampled gets succeed; verifies replication hits full K under 0%. 
  * **With drop**: replication ≥ 1 (origin), several vantage-point gets should still succeed deterministically under the seed (also logs an expected lower bound). 
  * **Many keys / different origins** with your current `-m4.drop` default—cross-checks retrieval robustness. 

From the package with the tests (the simulator lives with the kademlia code):

```powershell
# Default (1000 nodes, 10% drop, seed 1337)
go test . -count=1 -timeout=180s -run '^TestM4_Simulation_'
```

Turn off drop:

```powershell
go test . -count=1 -timeout=180s -run '^TestM4_Simulation_NoDrop_' -m4.drop=0
```

Crank it up (2000 nodes, 35% drop, deterministic seed):

```powershell
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_WithDrop_StillRetrievable$' -m4.nodes=2000 -m4.drop=35 -m4.seed=42
```

Edge cases:

```powershell
# Validate input sanitization & key-shape handling (invalid hex)
go test . -count=1 -timeout=60s -run '^TestM4_Simulation_InvalidKey_NotFound$'
```

* ✅ **Is**: a **test-only** simulation of lossy networks. It’s deterministic (seeded PRNG), fast, and lets you emulate 1000–2000 nodes without binding real sockets. Drop is modeled on the logical edges: STORE deliveries and FIND_VALUE request/response legs. 

* ❌ **Is not**: real UDP loss in runtime network code. The production `network.go` isn’t modified to drop packets. 

* We have packet drop in the M4 simulation, and it’s applied on both **replication** and **lookup** codepaths with a deterministic PRNG and a user-settable `-m4.drop` percent. See `dropped()`, its use in `simPut` and `simGet`, and the M4 tests.  

## Containerization (M5): run 50+ containers on one machine

Compose spins one **seed** plus **N knodes** on a private bridge network. Entrypoint computes the **container IP** and announces it (`-addr <ip:9000>`). The image builds in **GOPATH mode** and mirrors `labs/kademlia` to `/go/src/d7024e/kademlia`, matching imports like `d7024e/kademlia`. **You can scale to ≥50** with one flag. 

**Run from `d7024e\labs` (recommended):**

```powershell
# Build images
docker compose build --no-cache

# Bring up 1 seed + 50 nodes
docker compose up -d --scale knode=50

# See containers and follow seed logs
docker compose ps
docker compose logs -f seed
```

**Interact (seed has TTY):**

```powershell
docker attach $(docker compose ps -q seed)
# in the CLI:
put hello container world   # -> prints 40-hex key
# Ctrl+P, Ctrl+Q to detach without killing the seed
```

**Scale up/down:**

```powershell
docker compose up -d --scale knode=100   # scale up
docker compose up -d --scale knode=20    # scale down
```

**Tear down:**

```powershell
docker compose down        # remove containers + network
docker compose down -v     # also remove anonymous volumes
```

**Why this setup works:**

* Every node announces its **real container IP**, so UDP is routable inside the bridge.
* Build context is the repo root; Dockerfile/compose are under `labs/`.
* Entrypoint handles `seed` vs `node` startup, waits for Docker DNS on `seed`.
  (More troubleshooting and optional manual `docker run` flow in the Docker guide.) 

---

## Demos (end-to-end)

**Local (two terminals):**

```bash
# T1
go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9001
# T2
go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9002 -bootstrap 127.0.0.1:9001
# In T1 or T2:
put lorem ipsum
# copy key; on the other terminal:
get <that 40-hex key>
```

**Containerized (seed attach):**

```powershell
docker attach $(docker compose ps -q seed)
put hello 50-node net
# test a get on any knode (or just watch logs for FIND_VALUE)
```

(Behavior improvements—republisher + path caching + response routing—and their verification are described in §2.) 

---

* **Spec alignment:** All mandatory goals M1–M7 have concrete implementations and commands; qualifying goals like U1–U6 can layer on top (TTL/refresh/forget, REST API, higher coverage, full routing tree). Cite this handbook + course spec in the lab report. 
* **Operational sanity:** With the fixes, lookups will not silently time out if peers respond; buckets self-heal; values placed before topology changes eventually migrate to the responsible region. 
* **Thread-safety story:** Locks around data, never around network; single socket reader; bounded channels + timeouts; copy semantics for stored bytes. Run `-race` to validate. 
* **Coverage:** Commands to generate numbers and HTML are in §5; Windows-robust one-liners are provided. 
* **Debuggability:** Delve recipes for M1/M2 with precise break/trace points (Windows included). 
* **Scalability:** M4 simulator covers 1000–2000 nodes with drop%; M5 brings 50–100+ real containers.  

---

## Appendix A — One-page quickstart

```powershell
# from d7024e\labs
docker compose build --no-cache
docker compose up -d --scale knode=50
docker compose ps
docker compose logs -f seed
docker attach $(docker compose ps -q seed)   # put/get here; Ctrl+P,Ctrl+Q to detach
```



---

## Appendix B — M4 simulator commands (copy/paste)

```powershell
cd .\labs\kademlia
go test . -count=1 -timeout=180s -run '^TestM4_Simulation_'
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_' "-m4.nodes=2000" "-m4.drop=30" "-m4.seed=42"
```



---

## Appendix C — Coverage on Windows (robust)

```powershell
cd C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia
$path = "$pwd\coverage.out"
cmd /c "go test . -count=1 -timeout=120s -covermode=atomic -coverpkg=./... -coverprofile=""$path"""
go tool cover -func "$path"
go tool cover -html "$path" -o "$pwd\coverage.html"
```



---

## Appendix D — Delve “tour” (paste into `(dlv)`)

```
clearall
b (*Kademlia).Put
b (*Kademlia).Get
trace (*Network).sendStoreTo
trace (*Network).handleStore
trace (*Network).sendFindValueTo
trace (*Network).handleFindValue
config max-string-len 256
c
```



---

**That’s the whole project in one place:** the spec targets, what changed and why, how we keep it thread-safe under churn, how to test/simulate/cover/debug, and how to bring up a 50+ container network that behaves like a real DHT.

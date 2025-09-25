Here’s the straight answer for **M7**—what we do, where we do it, how, and why—focused on **concurrency** (parallel message handling) and **thread safety** (no data races, consistent state). I’m mapping each point to the exact code paths you have.

---

# What we do (concurrency primitives in use)

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

# Where we do it (by subsystem)

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

---

# How we guarantee thread safety (concrete invariants)

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

---

# Why this design (trade-offs & failure modes we avoid)

* **Throughput & latency:** α-parallelism + per-peer goroutines give you the expected sub-linear lookup latency while preserving correctness. If a subset of peers are slow or unresponsive, timeouts isolate them without stalling the whole lookup. 

* **Simplicity where it counts:** one read loop, small critical sections with clear ownership. Locks wrap only data structures, never the network. That makes deadlocks unlikely and debuggable if they ever appear. 

* **Correctness under churn:**

  * Routing updates from *any* message handler are safe (handlers run on the read-loop goroutine, but the table is protected by RWMutex).
  * Evictions probe liveness without risking lock deadlocks.
  * Republishing runs concurrently but respects data-structure locks and never blocks readers for long. 

* **Defensive coding:** non-blocking delivery to waiters + timeouts mean an application bug on the caller side can’t wedge the network loop. 

---

# If you want to validate this (quick checklist)

* Run with the race detector:
  `go test ./labs/kademlia -race` and `go run -race ./labs/kademlia/cmd/cli ...`
  You should see **no race reports** under normal put/get, bootstrapping, and eviction tests.

* Turn on your debug prints (NET/GET/REPLICATE) and hammer lookups while starting/stopping peers; the node should keep making progress, and the read loop should never stall.

---

## File anchors (for reviewers)

* **Networking:** read loop, inflight map, RPC helpers, timeouts, Close. 
* **Lookups & Store:** α-parallel fan-out, convergence, thread-safe value store, republisher goroutine. 
* **Routing:** RWMutex on table, LRU buckets, eviction/ping outside lock, closest-contacts query under RLock. 
* **CLI:** single-threaded front-end. 
* **Wire types / message kinds:** shared envelope format used across goroutines. 

That’s the concurrency story: **concurrent message handling** with **goroutines + channels**, and **thread safety** via **scoped locks**, **timeouts**, and **no network under locks**.

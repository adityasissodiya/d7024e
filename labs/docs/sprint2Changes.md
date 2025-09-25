# What we fixed

Two issues were making the DHT behave poorly:

## 1) New nodes couldn’t find values stored **before** they joined

**Symptom:** Start node **A**, `put "x"` → get a key. Start node **B**, bootstrap to A, then `get <key>` on B ⇒ `NOTFOUND`.

**Root causes:**

* We only replicated on **put time** (one shot). No periodic **republish**, so data never “migrated” to the *current* K-closest nodes when the network changed.
* No **path caching**, so successful lookups didn’t seed closer nodes.
* The UDP `readLoop` didn’t route `FIND_VALUE_OK` / `STORE_OK` back to waiting goroutines, so some lookups timed out even when peers responded.

**What we changed:**

* **Response routing:** `network.go` `readLoop` now also forwards `msgFindValueOK` and `msgStoreOK` to the pending request channels (in addition to `PONG`/`FIND_NODE_OK`).
  *File:* `labs/kademlia/network.go` (read loop).
* **Republisher:** a background task that periodically republishes all **origin** keys to the **current** K-closest peers. This is the Kademlia way to achieve “eventual correctness” of placement as nodes join/leave.
  *Files:* `labs/kademlia/kademlia.go` (`originKeys`, `republisher()`, `republishOwnedKeys()`).
* **Unified replication helper:** `replicateToClosest(key, id, value)` to do the “find current closest + STORE” both on initial `Put` and during republish.
  *File:* `labs/kademlia/kademlia.go`.
* **Path caching (lightweight):** after a successful `Get`, we also `STORE` the value to the **closest node we actually queried** (excluding the responder/self). This quickly seeds the right region even before the republisher runs.
  *File:* `labs/kademlia/kademlia.go` (`Get`).

**Tuning knobs:**

* `Kademlia.republishInterval` (default we picked: 15m for demos; set to 24h for production semantics).
* `alpha` (parallelism), `timeoutRPC`.

**How to verify (2 terminals):**

1. Start **A**: `go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9001`
2. On A: `put hello` → get `<key>`.
3. Start **B**: `go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9002 -bootstrap 127.0.0.1:9001`
4. On B: `get <key>`

   * Before these fixes: `NOTFOUND`.
   * Now: either it finds A immediately (and path-caches), or the republisher pushes the value to B/nearby nodes within one interval and subsequent `get` is a hit.

---

## 2) Routing table buckets never evicted stale nodes

**Symptom:** Once a bucket reached capacity, **new contacts were silently dropped**. Buckets never pinged/evicted dead entries, so the table went stale and lookups missed live peers.

**Root cause:** `bucket.AddContact` only move-to-front or push; no eviction logic. `RoutingTable.AddContact` just delegated blindly.

**What we changed (Kademlia-faithful):**

* **LRU ping/evict policy:** When a bucket is full and a new contact arrives:

  1. **Ping** the **least-recently seen** (LRU).
  2. If **no response** → **evict** LRU, insert newcomer (MRU).
  3. If **responds** → keep LRU (move to MRU), **drop** the newcomer into a small **replacement cache**.
* **Do pings outside locks:** We wire a `pingFunc` into the routing table so the liveness probe happens outside the mutex (no deadlocks).
* **Replacement cache:** per-bucket ring to remember recent overflow contacts; they can be promoted when a real eviction happens.

**Where:**

* *Files:*

  * `labs/kademlia/routingtable.go` — rewrite `AddContact` to implement the policy; add `SetPingFunc`.
  * `labs/kademlia/network.go` — new `PingWait(contact, timeout) bool` used by the table’s eviction logic.
  * `labs/kademlia/bucket.go` — add a tiny `repl` (replacement) cache with `addReplacement`/`popReplacement`.

**Tests added:**

* `labs/kademlia/routingtable_test.go`:

  * **Dead-LRU** ⇒ newcomer **replaces** LRU (size stays == `bucketSize`).
  * **Alive-LRU** ⇒ LRU kept/moved to MRU, newcomer **not** inserted (in replacement cache).
  * **Move-to-front** sanity when a known contact is seen again.

Run: `go test ./labs/kademlia -run RoutingTable -v`

---

## Upgrade notes

* Existing data placed before these changes will **migrate** to currently-closest nodes over time (republisher). If you’re impatient, re-`put` the values or shorten `republishInterval` during testing.
* We don’t implement TTL/expiry yet; add that if you want strict Kademlia semantics (values expire unless republished).
* Path caching stores to **one** best candidate by default; increase fan-out if you want faster convergence.

---

* **Discoverability:** New nodes now **eventually** find old data without manual re-publishing.
* **Fresh routing:** Buckets self-heal; dead peers get evicted, live newcomers get a slot.
* **Operational sanity:** Lookups aren’t silently timing out when peers respond; replication and caching are explicit and observable.


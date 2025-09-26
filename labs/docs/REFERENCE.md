# D7024E Kademlia — Technical Reference

## Quick index

`startup` · `bootstrap` · `routing table` · `bucket` · `LRU eviction` · `replacement cache` · `PING/PONG` · `FIND_NODE` · `FIND_VALUE` · `STORE` · `MsgID / inflight` · `alpha` · `K / bucketSize` · `XOR distance` · `Put` · `Get` · `replicateToClosest` · `path caching` · `republisher` · `timeouts` · `no network under locks` · `concurrency` · `in-memory store` · `packet drop (M4)` · `compose / containers (M5)` · `announce vs bind` · `debug prints` · `-race` · `coverage`

---

## Core constants & terms

* **K / `bucketSize`**: replication factor & k-bucket capacity (typ. 20).
* **α (`alpha`)**: parallelism per lookup step (typ. 3).
* **ID**: 160-bit (`KademliaID`).
  **Distance** = XOR(idA, idB); compare as big-endian integer.
* **Timeouts**: per-RPC wait (e.g., ~800ms).

“We replicate to **K closest by XOR**. Lookups query **α peers in parallel** and keep stepping toward smaller XOR distance.”

---

## Startup / Bootstrap

**One-liner:** Node binds UDP, starts a read loop, initializes a routing table. If `-bootstrap` is set, it pings that node and runs a warmup lookup to learn neighbors.

**Where in code:**

* `cmd/cli/main.go` → parses `-addr`, `-bootstrap`, `-id` → calls `NewKademlia`.
* `kademlia.go::NewKademlia` → create routing table, `NewNetwork`, start republisher, wire `SetPingFunc`.
* `network.go::NewNetwork` → bind UDP, start `readLoop()`.
* (bootstrap) `network.go::SendPingMessage` then `kademlia.go::LookupContact(...)`.

**What to say:** “We learn peers by **PING** and **FIND_NODE** during bootstrap; that fills k-buckets across the ID space.”

---

## RPCs (wire protocol)

All messages share an **envelope** (`wire.go`) with: `Type`, `From` (Contact), `MsgID`, and type-specific fields.

* **PING / PONG** — liveness.
* **FIND_NODE / …_OK(contacts)** — return contacts nearest to a target ID.
* **FIND_VALUE / …_OK(value|contacts)** — return value if held, else contacts toward the key.
* **STORE / STORE_OK** — write (key,value) to a peer.

**What to say:** “We correlate every request with a `MsgID` and park a waiter channel in an `inflight` map; the read loop delivers the matching response into that channel.”

---

## Read loop & message correlation

**One-liner:** Single goroutine reads UDP, parses envelopes, routes **responses** to waiters, and calls **handlers** for requests.

**Where:** `network.go::readLoop`, `send`, `inflight` map.

**What to say:** “The reader never blocks: waiter channels are size-1 and we do non-blocking sends; timeouts clean up `inflight` to avoid leaks.”

---

## Routing table / Buckets

**One-liner:** k-buckets are **LRU lists** ordered by “recently seen,” indexed by XOR prefix length.

**Where:** `routingtable.go`, `bucket.go`.

**AddContact (LRU eviction policy):**

* If **present** → move to **front (MRU)**.
* If **space** → push front.
* If **full** → **ping LRU** **outside the lock**:

  * **No PONG** → evict LRU, insert newcomer (front).
  * **PONG** → keep LRU (move to front), put newcomer in **replacement cache**.

**Why:** keeps reliable peers, purges dead ones, avoids deadlocks by not doing network under table locks.

---

## Put (replication)

**One-liner:** Hash data → key; store locally; find **current K closest** to key; **STORE** to each.

**Where:**

* `kademlia.go::Put` → `storeLocal` → mark origin → `replicateToClosest`.
* `replicateToClosest` → `LookupContact(target=keyID)` (refresh neighborhood) → `routingTable.FindClosestContacts` → `network.sendStoreTo`.

**Wire path:** `sendStoreTo` builds `STORE`, registers `inflight[msgID]`, `send()`. Handler `handleStore` persists and replies `STORE_OK`.

**What to say:** “Placement is **deterministic**: K nodes closest by XOR to the key, plus the origin. We refresh the routing view around the key before sending.”

---

## Get (lookup + path caching)

**One-liner:** Try local; else iterative lookup toward the key: query **α closest unvisited**; if none return value, merge contacts and repeat. On success, **cache locally** and **path-cache** to one close node queried.

**Where:**

* `kademlia.go::Get` → `loadLocal` → iterative α batches → `network.sendFindValueTo`.
* On value: `storeLocal` (cache) + one `sendStoreTo` to a good on-path node.

**Wire path:** `FIND_VALUE` request; handler replies either `value` or `closest contacts`.

**What to say:** “Not gossip: we **target** nearer contacts every step. First success seeds caches; origin’s republisher handles long-term migration.”

---

## Republisher (eventual placement)

**One-liner:** Background goroutine that periodically re-sends origin keys to the **current** K closest nodes (so data migrates when the network changes).

**Where:** `kademlia.go::republisher`, `originKeys`, `republishInterval`, calls `replicateToClosest`.

**What to say:** “Ensures new nodes later become responsible for older keys—**eventual correctness** of placement.”

---

## Path caching (read-time seeding)

**One-liner:** After a successful `get`, store the value on **one** close node on the path (besides the responder and self).

**Where:** inside `kademlia.go::Get` success path.

**What to say:** “Speeds up subsequent reads without flooding; still not gossip.”

---

## Concurrency & thread-safety

* **Goroutines**: read loop, each RPC, republisher.
* **Channels**: `inflight[msgID]` waiter per request.
* **Locks**: `routingTable.mu` (R/W), `network.mu` (inflight), `storeMu` (values).
* **Rules**:

  * **No network under locks** (eviction pings & replication happen after releasing locks).
  * **Non-blocking** delivery in read loop.
  * **Timeouts** on every wait.
  * **Copy on store/load** for values (no shared slices).

**What to say:** “We avoid deadlocks and data races by scoping locks to data structures and never mixing them with network I/O.”

---

## Distance / Closest contacts

**One-liner:** XOR metric; lower is closer. To find K closest: compute distances, keep the smallest K.

**Where:** `kademliaid.go` (distance), `routingtable.go::FindClosestContacts`.

**What to say:** “Our lookups converge because every step reduces XOR distance to the target.”

---

## Simulation (M4) — packet drop

**One-liner:** Test-only simulator that models **packet drop %** on replication and lookup paths with a deterministic RNG (no changes to real UDP).

**Where:** `m4_simulation_test.go`, `sim_network_test.go`.
Knobs: `-m4.nodes`, `-m4.drop` (percent), `-m4.seed`.

**What to say:** “We drop on STORE sends and FIND_VALUE request/response legs; origin always keeps a copy; used to prove robustness at 1000–2000 nodes.”

**Run:**

```bash
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_' -m4.nodes=2000 -m4.drop=30 -m4.seed=42
```

---

## Containerization (M5) — 50+ nodes

**One-liner:** Docker Compose brings up 1 **seed** + N **knodes** on a bridge. Entrypoint computes the **container IP** and announces it (`-addr <ip:9000>`). Scale with one flag.

**Where:** `labs/Dockerfile`, `labs/docker-compose.yml`, `labs/docker/entrypoint.sh`.

**Key concept — “announce vs bind”:** we must **announce** a routable address (container IP); binding to 0.0.0.0 is fine, announcing 0.0.0.0 is wrong.

**Run (from `labs/`):**

```bash
docker compose build
docker compose up -d --scale knode=50
docker compose ps
docker compose logs -f seed
docker attach $(docker compose ps -q seed)   # put/get here; Ctrl+P, Ctrl+Q to detach
```

---

## Not gossip

**One-liner:** Kademlia ≠ gossip. We **don’t** periodically spread membership randomly. We **target** lookups and replicate to **K responsible** nodes; learning happens as a side-effect of actual queries.

**What to say:** “No epidemic push/pull; peers are learned via PING/FIND_* on real routes.”

---

## Typical viva questions → crisp answers

* **Q:** *Where do values live?*
  **A:** On the **K nodes closest by XOR** to the **key** (plus origin). Republisher keeps it there as the network evolves.

* **Q:** *How do nodes learn peers?*
  **A:** PING + **FIND_NODE** (bootstrap), **FIND_* responses** and **incoming requests** (we add `From`), with **LRU eviction** to keep buckets fresh.

* **Q:** *Why α parallelism?*
  **A:** Tolerates slow/lost peers without stalling; each step queries α nearest unvisited to reduce XOR distance.

* **Q:** *What prevents deadlocks?*
  **A:** No network under locks; read loop non-blocking; timeouts everywhere.

* **Q:** *Is it eventual consistency?*
  **A:** For placement, yes—**republisher** eventually moves data to the **current** K closest.

* **Q:** *What happens if the bucket is full?*
  **A:** **Ping LRU**; dead → evict/insert; alive → keep LRU, newcomer to **replacement cache**.

* **Q:** *Why NOTFOUND sometimes after topology changes?*
  **A:** If a value was put when the network was small, replicas may not be known to a new node until **path caching**/ **republisher** runs (now fixed).

* **Q:** *How do you correlate responses?*
  **A:** `MsgID` → `inflight` map → waiter channel; read loop pushes responses into the matching channel.

---

## Debug / Observability quickies

Add these prints while demoing:

* `network.go::send`: `fmt.Printf("[NET] => %s id=%s to=%s\n", env.Type, env.MsgID, to)`
* `network.go::readLoop`: `fmt.Printf("[NET] <= %s id=%s from=%s\n", env.Type, env.MsgID, from)`
* `kademlia.go::Put/Get/replicateToClosest`: log key, candidates, chosen peers.
* `routingtable.go::AddContact`: log “insert/evict/keep LRU”.

**Race detector:**

```bash
go test ./labs/kademlia -race
go run -race ./labs/kademlia/cmd/cli -addr 127.0.0.1:9001
```

**Coverage (quick):**

```bash
cd labs/kademlia
go test . -count=1 -timeout=120s -cover
```

---

## File-by-file jump table

* **CLI**: `labs/kademlia/cmd/cli/main.go`, `labs/kademlia/cli.go`
* **Core**: `kademlia.go` (Put, Get, republisher, replicateToClosest)
* **Routing**: `routingtable.go` (AddContact, FindClosestContacts), `bucket.go`
* **Network**: `network.go` (readLoop, send, SendPingMessage, sendFindValueTo, sendStoreTo)
* **Wire**: `wire.go` (envelope, marshal/unmarshal)
* **IDs**: `kademliaid.go` (parse, XOR distance)
* **Simulation**: `m4_simulation_test.go`, `sim_network_test.go`
* **Containers**: `labs/Dockerfile`, `labs/docker-compose.yml`, `labs/docker/entrypoint.sh`

---

## Two canonical flows (soundbite versions)

**Put:** `CLI → Kademlia.Put → storeLocal → LookupContact(key) → FindClosestContacts → send STORE to K peers → replicas reply STORE_OK → republisher maintains`.
**Get:** `CLI → Kademlia.Get → loadLocal ? hit : iterative α× send FIND_VALUE → value ? cache+path-cache : merge contacts & repeat → NOTFOUND or success`.

---
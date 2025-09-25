This project is a **distributed key-value store**. You give it some text, it **hashes** it (SHA-1) to a 40-hex “key” and stores the text across the network. Later, anyone with the key can fetch the text. There’s no central server; **many small programs (nodes)** cooperate to store and find stuff. The algorithm they use to find each other fast is **Kademlia**.

Think of it like a giant, decentralized phonebook:

* **Key** = the phone number (derived from your text)
* **Value** = the contact info (your text)
* **Nodes** = people who each know a smart subset of the phonebook and can route you toward the right person quickly.

## What a node actually does

Each node:

1. **Listens on UDP** (lightweight network packets) at an address like `127.0.0.1:9001` or a Docker IP.
2. Keeps a **routing table**: a sorted list of peers organized by how “far” their IDs are from yours (XOR distance).
3. Speaks four tiny RPCs:

   * `PING` / `PONG` – “you alive?”
   * `FIND_NODE` – “who’s close to this ID?”
   * `STORE` – “please save this (key,value)”
   * `FIND_VALUE` – “got this key?”
4. Stores values in a simple **in-memory map**.

## How data flows

### put <text>

1. Node hashes your text → **key**.
2. It **stores locally** right away (always keep a copy).
3. It finds the **K closest nodes** to that key (via the routing table + lookups).
4. It sends them **STORE** so they also keep the value.
   Result: your data lives on the nodes that are mathematically “closest” to the key.

### get <key>

1. Check **local cache**; if present, return.
2. If not, start an **iterative search**: ask a few close nodes in parallel, they return either the **value** (done) or **closer nodes** to try next.
3. On success the requester **caches** the value locally (so next time it’s faster).

Two extra behaviors we added so the network doesn’t act dumb:

* **Republisher**: every so often, the original uploader republishes values to whatever nodes are **currently** closest. That way, if new nodes join later, the data migrates to the “right” place.
* **Path caching**: when a `get` finally finds a value, we also stash it on a close node along the way. First successful read helps future reads.

## How nodes find each other (routing table)

* The table is split into “**buckets**” by distance ranges.
* Each bucket is **LRU** (least-recently-used at the back, most-recent at the front).
* If a bucket is **full** and we see a new node:

  1. **Ping the LRU**.
  2. If it’s **dead** → evict it, insert the newcomer.
  3. If it’s **alive** → keep it, and put the newcomer in a small **replacement cache**.
     Bottom line: we don’t throw away reliable nodes for random newcomers, but we will **replace dead ones**.

## Concurrency

* **One goroutine** reads from the UDP socket and fans requests/responses to the right places.
* **Per-peer RPCs** run in their own goroutines (parallel lookups).
* **Channels** glue senders to responses (each request gets a tiny channel).
* **Mutexes** protect shared stuff (routing table, inflight map, value store).
* We **don’t** do network I/O while holding big locks (prevents deadlocks).
* Everything has **timeouts**; if a peer is slow or gone, we move on.


## Running it (two ways)

### Quick local

Terminal A:

```bash
go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9001
```

Terminal B:

```bash
go run ./labs/kademlia/cmd/cli -addr 127.0.0.1:9002 -bootstrap 127.0.0.1:9001
```

Then in either: `put hello`, copy the key, `get <key>` in the other.

### Big demo (50+ containers)

We have:

* `labs/Dockerfile`
* `labs/docker-compose.yml`
* `labs/docker/entrypoint.sh`

From `d7024e/labs`:

```powershell
docker compose build
docker compose up -d --scale knode=50
docker compose ps
docker compose logs -f seed
# attach to the seed and type put/get:
docker attach $(docker compose ps -q seed)
```

Each container **announces its own IP** so UDP packets actually reach it.

# Testing at scale (without opening 1000 real sockets)

There’s a **simulator** in tests:

* You can spin **1000–2000 fake nodes** in-process.
* It can simulate **packet drop** (e.g., 10–35%) on STORE and FIND_VALUE requests/responses.
* It’s deterministic (seeded), so you can reproduce runs.

Example:

```bash
cd ./labs/kademlia
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_' -m4.nodes=2000 -m4.drop=30 -m4.seed=42
```

## Where things live (files)

* **CLI**: `labs/kademlia/cmd/cli`
* **Network (UDP + RPC handlers)**: `network.go`
* **Routing table & buckets**: `routingtable.go`, `bucket.go`
* **IDs & distances**: `kademliaid.go`
* **Kademlia core (Put/Get/republisher/path cache)**: `kademlia.go`
* **Wire formats**: `wire.go`
* **Container stuff**: `labs/Dockerfile`, `labs/docker-compose.yml`, `labs/docker/entrypoint.sh`
* **Simulator tests**: `m4_simulation_test.go`, `sim_network_test.go`

## What to remember (cheat sheet)

* “**Closest by XOR distance**” decides where data belongs.
* **put** writes locally + replicates to the K closest.
* **get** walks toward the key; caches on success.
* **Republisher** keeps placement correct as the network changes.
* **LRU eviction** keeps the routing table fresh.
* **Goroutines + channels + locks + timeouts** = concurrency without drama.
* For scale, use **docker compose**; for stress, use the **simulator**.
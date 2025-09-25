# D7024E Kademlia

This lab is a full Kademlia DHT implementation with a CLI, tests, large-scale emulation, concurrency/thread-safety, and containerization.

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

---

## 1) Code orientation (what’s where)

* `labs/kademlia/…`: core DHT—routing table, UDP network, iterative lookups, store/cache, CLI.
* `labs/kademlia/cmd/cli`: the node/CLI entrypoint (`put/get/exit`).
* Tests:

  * Unit & integration tests for M1–M3 live alongside the code.
  * Large-scale simulator tests (M4) provide 1000–2000 node emulation with packet drop knobs; they don’t touch production code. 
* Container artifacts:

  * `labs/Dockerfile`, `labs/docker-compose.yml`, `labs/docker/entrypoint.sh`. 

Two correctness/operational problems were addressed:

### New nodes couldn’t find values stored **before** they joined

**Root causes:** one-shot replication at put-time only; no periodic republish; no path caching; and read-loop didn’t route some OK responses back to waiters.
**Fixes:**

* Route `FIND_VALUE_OK`/`STORE_OK` in `readLoop` to the proper inflight waiter.
* Add a **republisher** goroutine to periodically re-replicate origin keys to **current** K-closest nodes (eventual placement).
* Add **path caching** on successful `get` (store to the best candidate we queried).
* Provide a unified `replicateToClosest()` helper used by both initial `put` and republish. 

**Result:** New nodes **eventually** discover old data without manual re-`put`; often immediately via path caching. 

### Routing buckets didn’t evict stale nodes

**Root cause:** no Kademlia LRU ping/evict policy; full buckets silently dropped newcomers, letting dead entries linger.
**Fixes:** Full Kademlia behavior: ping **LRU**; if dead, **evict** and insert newcomer; if alive, **keep** LRU and cache newcomer in a **replacement cache**. Pings happen **outside** the routing table lock. Tests verify both dead-LRU and alive-LRU cases. 

## 2) Concurrency & thread-safety (M7)

**Concurrency mechanisms used:**

* **Goroutines** for UDP read-loop and per-peer RPCs (α-parallel fan-out during lookups).
* **Channels** to correlate request/response via inflight map keyed by MsgID.
* **Mutex/RWMutex** to guard shared structures (routing table, inflight map, value store).
* **Timeouts** on all waits to prevent leaks and livelocks.
* **Background republisher** goroutine—ticker-driven, never performs network I/O while holding data locks. 

**Thread-safety invariants:**

* Every mutable map/list is behind a lock; **no network I/O while holding high-level locks** (evictions ping outside the lock; republisher releases locks before RPCs).
* Read-loop never blocks: waiter channels are size-1; select with default avoids deadlocks if caller timed out.
* Store loads/stores copy byte slices to avoid cross-goroutine aliasing.
* Single socket reader (the read-loop) simplifies synchronization. 

Predictable throughput/latency, low deadlock risk, correctness under churn (routing updates safe; eviction probes liveness safely; republishing respects locks). Validation: run with `-race`; hammer lookups while starting/stopping peers—read-loop should never stall. 

## 3) Debugging with Delve (Windows-proof, copy/paste)

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

## 4) Unit testing & coverage (M4)

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

## 5) Large-scale emulation (M4: 1000–2000 nodes + packet drop)

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

Short answer: you already have packet drop in the **M4 simulator**. It’s implemented in the test harness (not in the real UDP stack), and it’s applied in three places:

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

## 6) Containerization (M5): run 50+ containers on one machine

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

## 7) Demos (end-to-end)

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

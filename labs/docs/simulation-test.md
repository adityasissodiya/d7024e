# M4 – Unit Testing & Large-Scale Network Emulation

This doc explains exactly what our **M4** work does, where it lives, and how to run it so examiners can verify the rubric:

> **Rubric**
>
> * Emulate **≥ 1000 nodes**
> * Include **packet dropping functionality**
> * Make both **node count** and **drop %** easy to change
> * **≥ 50%** coverage overall

We meet all of the above. Coverage of the `kademlia` package is \~88% in our runs; the simulator adds the 1000-node + drop tests without touching production code.

---

## What’s implemented

**Test-only simulator** (no changes to runtime code):

* **Files**

  * `labs/kademlia/sim_network_test.go` – simulation scaffolding
  * `labs/kademlia/m4_simulation_test.go` – M4 tests that drive 1000–2000 nodes
* **Features**

  * Emulates **N** nodes (default **1000**) with real `KademliaID`s
  * **Replicates to K closest** for puts (uses package `bucketSize`)
  * **Drop %** applied on message “deliveries” (both STORE and FIND\_VALUE paths)
  * Deterministic **PRNG seed** so results are stable
* **Flags (test-only)**

  * `-m4.nodes` — number of emulated nodes (default: `1000`)
  * `-m4.drop` — packet drop percentage 0..100 (default: `10`)
  * `-m4.seed` — PRNG seed (default: `1337`)

---

## How to run (Windows PowerShell & macOS/Linux)

> **Important PowerShell note:** flags with dots (e.g., `-m4.nodes=2000`) must be quoted in PS.
> The commands below are copy/paste-ready.

### Quick run (defaults: 1000 nodes, 10% drop)

```powershell
cd .\labs\kademlia
go test . -count=1 -timeout=180s -run '^TestM4_Simulation_'
```

### Scale up to 2000 nodes, 30% drop (deterministic seed)

```powershell
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_' "-m4.nodes=2000" "-m4.drop=30" "-m4.seed=42"
```

### Show verbose logs

```powershell
go test . -v -count=1 -timeout=300s -run '^TestM4_Simulation_' "-m4.nodes=2000" "-m4.drop=30"
```

### macOS/Linux (no quoting needed)

```bash
cd labs/kademlia
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_' -m4.nodes=2000 -m4.drop=30 -m4.seed=42
```

---

## What the tests do

* **`TestM4_Simulation_NoDrop_AllGetsSucceed`**
  1000 nodes, **0% drop** → replication reaches **K**; GETs from multiple vantage points succeed.

* **`TestM4_Simulation_WithDrop_StillRetrievable`**
  1000 nodes, **configurable drop%** (default 10) → still retrievable from several vantage points; logs how many replicas landed.

* **`TestM4_Simulation_InvalidKey_NotFound`**
  Bad key shapes (not 40 hex chars / non-hex) → safely rejected.

* **`TestM4_Simulation_ManyKeys_Distributed`**
  Multiple different origins/payloads across a large cluster → each key remains retrievable from at least one vantage point.

* **`TestM4_Simulation_KeyShape_Sanity`**
  Sanity around hex length/format; ensures no panics.

---

## Why a simulator (and not 1000 real UDP sockets)?

* Spinning **1000+** actual UDP sockets on localhost is brittle (FD limits, port churn, timing flakiness).
* The simulator is **fast, deterministic**, and keeps **production code untouched**, while still exercising the **K-closest replication** and **FIND\_VALUE retrieval** logic with **packet loss**.

---

## Coverage

Overall coverage remains above the required **50%**.

To measure:

```powershell
# from the repo root or labs/kademlia
go test ./... -covermode=atomic -coverpkg=./... -coverprofile=coverage.out
go tool cover -func=coverage.out
# optional HTML
go tool cover -html=coverage.out -o coverage.html
```

---

## Knobs (easy to change)

* **Nodes:** `-m4.nodes=1000` (try 1500/2000 if you want)
* **Drop %:** `-m4.drop=0..100` (e.g., 5, 10, 30)

  > Note: replication always includes the **origin**, so even high drop rates keep at least one copy.
* **Seed:** `-m4.seed=42` for reproducibility

Example (PowerShell):

```powershell
go test . -count=1 -timeout=300s -run '^TestM4_Simulation_' "-m4.nodes=1500" "-m4.drop=25" "-m4.seed=2025"
```

---

## Mapping to the rubric

| Requirement           | Where / How                                                                 |
| --------------------- | --------------------------------------------------------------------------- |
| ≥ 1000 nodes emulated | `sim_network_test.go` + `m4_simulation_test.go` (`-m4.nodes`, default 1000) |
| Packet dropping       | Simulator’s `dropPct` applied on deliveries (`-m4.drop`)                    |
| Easy to change        | Test flags (`-m4.nodes`, `-m4.drop`, `-m4.seed`)                            |
| ≥ 50% coverage        | `go test` coverage across package (≈88% in our runs)                        |
| Core logic tested     | PUT/GET, K-closest replication, retrieval under loss, key validation        |

---

## Troubleshooting

* **PowerShell: “flag provided but not defined: -m4”**
  Quote the flags: `"-m4.nodes=2000" "-m4.drop=30" "-m4.seed=42"`
  (Or use `-args` before custom flags.)

* **Timeouts**
  Increase `-timeout=300s` for very large runs.

* **Skipping in `-short`**
  Some M4 tests skip under `-short`. Don’t pass `-short` when grading M4.

---

That’s it. This suite gives you **scalable, deterministic** emulation (1000–2000 nodes) with a **packet loss knob**, clear commands for examiners, and keeps the runtime implementation clean.

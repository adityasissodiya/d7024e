# Debugging M1/M2 with Delve (dlv)

This doc shows **exact** steps to debug the M1 (network formation) and M2 (object distribution) code in this repo using Delve. It includes the pitfalls we already hit and the commands that actually work on Windows/PowerShell.

> Repo layout assumed: `d7024e/labs/kademlia` contains the code and tests (`m1_network_test.go`, `m2_value_test.go`).

---

## 0) Preconditions

* Go and Delve installed.
* Run everything from the **package directory**:
  `C:\Users\<you>\Downloads\D7024E\d7024e\labs\kademlia`

Sanity check:

```powershell
go env GOMOD
# should print a path; if it's empty you’re not in a module dir
```

---

## 1) Build a debug test binary (no optimizations)

**Why:** Avoids “debugging optimized function” weirdness and unstable breakpoints.

```powershell
# from: ...\d7024e\labs\kademlia
go test -c -gcflags=all="-N -l" -o m1m2.test.exe
```

> If you’re on Linux/macOS, drop the `.exe`.

---

## 2) Launch patterns that don’t break on Windows

### A) Reliable launch (PowerShell stop-parsing)

This passes all `-test.*` flags raw to the test binary:

```powershell
dlv exec .\m1m2.test.exe --% -- -test.run=TestM2_PutAndGet_SucceedsAcrossNetwork -test.v=true -test.count=1 -test.timeout=60s
```

### B) Alternative: set args inside dlv

```powershell
dlv exec .\m1m2.test.exe
# at (dlv):
args -test.run=TestM2_PutAndGet_SucceedsAcrossNetwork -test.v=true -test.count=1 -test.timeout=60s
r     # restart with those args
```

> Avoid `dlv test` from repo root. If you insist, point it at the package dir:
> `dlv test .\labs\kademlia -- -- -test.run=...` (equals `-test.run=` **required**).

---

## 3) Minimal dlv command set (you’ll use these constantly)

```
b <func>                  # set breakpoint
trace <func>              # print on entry/return, don’t stop
clearall                  # remove all break/trace points
bp                        # list break/trace points
c                         # continue
n / s / so                # next / step / step out
locals / args             # show locals / args
p <expr>                  # print expression
goroutines / goroutine N  # list / switch goroutine
stack                     # backtrace
rebuild                   # rebuild target keeping breakpoints
r                         # restart with same args
config max-string-len 256 # make printed strings readable
```

**Important:** Don’t type comments (`# ...`) after dlv commands.

---

## 4) Ready-to-use “tour” setups

### 4.1 Non-invasive M2 walkthrough (recommended for recording)

1. Launch (section 2A).
2. Paste this at `(dlv)`:

```
clearall

# Break only at high-level entry points
b (*Kademlia).Put
b (*Kademlia).Get

# Trace hot network paths so you don't perturb timeouts
trace (*Network).sendStoreTo
trace (*Network).handleStore
trace (*Network).sendFindValueTo
trace (*Network).handleFindValue

config max-string-len 256
bp
c
```

3. When it stops in `Put`:

```
n
p keyHex
# Optional: increase timeouts during demo to reduce deadlineExceeded noise
set kademlia.timeoutRPC = 3000000000   # 3s, nanoseconds
c
```

**What you’ll see (and say):**

* `Put` computes **key** (SHA-1), **stores locally**, then **replicates** to K nearest → trace shows `sendStoreTo → handleStore`.
* `Get` on another node: local miss → **α parallel FIND\_VALUE** → first value wins → requester **caches** (local `storeLocal`).

> If you want to filter traces to a single key, set conditions after you print `keyHex`:
>
> ```
> bp
> cond <tp_sendStoreTo>      keyHex == "<hex>"
> cond <tp_handleStore>      env.KeyHex == "<hex>"
> cond <tp_sendFindValueTo>  keyHex == "<hex>"
> cond <tp_handleFindValue>  env.KeyHex == "<hex>"
> ```

---

### 4.2 Invasive M2 step-through (if you must step into handlers)

Do the non-invasive setup, **then** also:

```
b (*Kademlia).storeLocal
b (*Kademlia).loadLocal
```

Before stepping into network handlers, **raise timeout**:

```
set kademlia.timeoutRPC = 3000000000  # 3s
```

Now step (`n`/`s`) through:

* Origin `storeLocal` (valueStore size should bump).
* Iterative lookup for K set.
* Per-peer `sendStoreTo` → peer `handleStore`.
* Requester `Get`: `loadLocal` miss → `sendFindValueTo` → peer `handleFindValue` → **cache** via `storeLocal`.

---

### 4.3 Quick M1 tour (Ping, Join, Node Lookup)

Run one of your M1 tests (examples):
`TestPingAddsBothSides`, `TestLookupFindsTargetInSmallNetwork`, or the Join-related test you have.

Launch:

```powershell
dlv exec .\m1m2.test.exe --% -- -test.run=TestPingAddsBothSides -test.v=true -test.count=1 -test.timeout=30s
```

At `(dlv)`:

```
clearall
b (*Network).SendPingMessage
b (*Network).handlePing
b (*Kademlia).Join
b (*Kademlia).LookupContact
trace (*Network).SendFindContactMessageTo
trace (*Network).handleFindNode
trace (*RoutingTable).FindClosestContacts
trace (*bucket).AddContact
config max-string-len 256
c
```

Narrate:

* `SendPingMessage` creates inflight channel, sends `PING`.
* Peer `handlePing` learns sender → `AddContact` → replies `PONG`.
* `Join` pings bootstrap then `LookupContact(selfID)` to warm the table.
* `SendFindContactMessageTo/handleFindNode` show iterative lookup traffic.

---

### 4.4 Concurrent GETs

```powershell
dlv exec .\m1m2.test.exe --% -- -test.run=TestM2_ConcurrentGets -test.v=true -test.count=1 -test.timeout=60s
```

At `(dlv)`:

```
clearall
b (*Kademlia).Get
trace (*Network).sendFindValueTo
config max-string-len 256
c
# when stopped in Get:
goroutines
goroutine <id>
stack
```

Narrate α-parallelism and per-request inflight demux (MsgID → channel).

---

### 4.5 Partial replication, still retrievable

```powershell
dlv exec .\m1m2.test.exe --% -- -test.run=TestM2_PartialReplication_SomeNodesDown -test.v=true -test.count=1 -test.timeout=60s
```

Use:

```
trace (*Network).sendStoreTo
trace (*Network).handleStore
c
```

Show that some `sendStoreTo` ops time out or don’t get `handleStore`, yet `Get` still succeeds because at least one designated node stored the value.

---

## 5) Optional helper files

### 5.1 `commands.dlv` (drop in `labs/kademlia`)

```text
clearall
# High-level stops
b (*Kademlia).Put
b (*Kademlia).Get
# Hot-path traces
trace (*Network).sendStoreTo
trace (*Network).handleStore
trace (*Network).sendFindValueTo
trace (*Network).handleFindValue
config max-string-len 256
bp
```

Use it:

```powershell
dlv exec .\m1m2.test.exe --% -- -test.run=TestM2_PutAndGet_SucceedsAcrossNetwork -test.v=true -test.count=1 -test.timeout=60s
# at (dlv):
source commands.dlv
c
```

### 5.2 Capture a session transcript

```text
transcript start dlv-session.txt
# ... do your demo ...
transcript stop
```

---

## 6) Troubleshooting

* **`go: cannot find main module`**
  You launched from repo root. `cd ...\labs\kademlia` **or** `dlv test .\labs\kademlia -- -- ...`.

* **`flag provided but not defined: -test`**
  PowerShell mangled args. Use **`--% --`** (stop-parsing) or set args via `args` + `r` inside dlv.

* **`Warning: debugging optimized function`**
  You forgot `-gcflags=all="-N -l"` when building the test binary.

* **“no source available”**
  You stepped into runtime/syscall. Use `so` (step out) or `n` until you’re back in our code.

* **Breakpoints never hit**
  Wrong symbol. Use `funcs` to list, then break on the exact printed name
  (e.g., `b d7024e/kademlia.(*Network).sendStoreTo`).

* **Trace spam**
  Use `clearall`, re-add only the traces you need, add `cond` filters on the key, and attach `on <bp> print ...` for focused logs.

* **Deadlines exceeded during recording**
  Temporarily raise timeouts: `set kademlia.timeoutRPC = 3000000000`.

---

## 7) Known-good test targets (what to run)

* M2 happy path:
  `-test.run=TestM2_PutAndGet_SucceedsAcrossNetwork`
* M2 partial replication:
  `-test.run=TestM2_PartialReplication_SomeNodesDown`
* M2 concurrent gets:
  `-test.run=TestM2_ConcurrentGets`
* M1 (examples):
  `-test.run=TestPingAddsBothSides`
  `-test.run=TestLookupFindsTargetInSmallNetwork`

> Use one at a time for clean debugging.

---

## 8) full demo flow

```powershell
cd C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia
go test -c -gcflags=all="-N -l" -o m1m2.test.exe
dlv exec .\m1m2.test.exe --% -- -test.run=TestM2_PutAndGet_SucceedsAcrossNetwork -test.v=true -test.count=1 -test.timeout=60s
# (dlv)
clearall
b (*Kademlia).Put
b (*Kademlia).Get
trace (*Network).sendStoreTo
trace (*Network).handleStore
trace (*Network).sendFindValueTo
trace (*Network).handleFindValue
config max-string-len 256
c
# when paused in Put:
n
p keyHex
set kademlia.timeoutRPC = 3000000000   # optional, demo-friendly
c
```

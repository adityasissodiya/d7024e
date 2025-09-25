# Docker Guide — Running a 50-node Kademlia network for `d7024e`

This doc shows **exact commands** to build, run, scale, and debug a containerized Kademlia network with **50+ nodes**, each in its **own container**, using Docker Compose. It also explains how the container setup maps to the Go codebase.

> **Assumptions**
>
> * Repo root: `C:\Users\adisis\Downloads\D7024E\d7024e` (Windows paths shown; on macOS/Linux replace `\` with `/`).
> * Files (already created as per our setup):
>
>   * `labs/Dockerfile`
>   * `labs/docker-compose.yml`
>   * `labs/docker/entrypoint.sh`

---

## 1) How it fits the Go/Kademlia project

* The **CLI** lives at `labs/kademlia/cmd/cli`. Each container runs **one CLI instance** (one node).
* The **entrypoint** (`labs/docker/entrypoint.sh`) computes the container’s **IP** on the Docker bridge (`172.x.y.z`) and **announces that exact IP** to the DHT using the `-addr` flag. This is critical for UDP reachability.
* Containers join a private **Docker bridge** network (`kadnet`). The **seed** service acts as the **bootstrap node** (DNS name `seed` inside the network).
* The Dockerfile builds with **GOPATH mode** (because the CLI imports `d7024e/kademlia`) and mirrors `labs/kademlia` to `/go/src/d7024e/kademlia` in the image. No source changes needed.

---

## 2) File map (what’s where)

```
d7024e/
├─ labs/
│  ├─ Dockerfile
│  ├─ docker-compose.yml
│  └─ docker/
│     └─ entrypoint.sh
└─ labs/kademlia/… (your Go code: network.go, routingtable.go, cmd/cli, etc.)
```

### `labs/Dockerfile` (key points)

* Builds in **GOPATH mode** (`GO111MODULE=off`).
* Copies the whole repo into the image.
* Mirrors `labs/kademlia` to `/go/src/d7024e/kademlia`.
* Builds the CLI binary `/app/kcli`.
* Uses `/app/entrypoint.sh` as `ENTRYPOINT`.

### `labs/docker-compose.yml`

* Defines services:

  * `seed` → runs a single bootstrap node (`command: ["seed"]`).
  * `knode` → the scalable node service (`command: ["node"]`).
* `build.context: ..` so the build sees the **repo root**.
* Both services use the same image and Dockerfile.

### `labs/docker/entrypoint.sh`

* Figures out container IP.
* For `seed`: runs `/app/kcli -addr <ip:9000>`.
* For `node`: waits for `seed` to resolve, then runs `/app/kcli -addr <ip:9000> -bootstrap seed:9000`.

---

## 3) Build & Run (copy/paste)

You can run everything **from `labs/`** (recommended), or from the **repo root** while pointing to the compose file. Pick one way and stick to it.

### Option A — run from `labs/` (recommended)

```powershell
# Go to the directory that contains docker-compose.yml
PS C:\Users\adisis\Downloads\D7024E\d7024e\labs> docker compose build --no-cache

# Bring up 1 seed + 50 nodes
PS C:\Users\adisis\Downloads\D7024E\d7024e\labs> docker compose up -d --scale knode=50

# See them running
PS C:\Users\adisis\Downloads\D7024E\d7024e\labs> docker compose ps

# Follow seed logs (you'll see PING/FIND_NODE/STORE/FIND_VALUE traffic)
PS C:\Users\adisis\Downloads\D7024E\d7024e\labs> docker compose logs -f seed
```

### Option B — run from repo root

```powershell
PS C:\Users\adisis\Downloads\D7024E\d7024e> docker compose -f labs\docker-compose.yml build --no-cache
PS C:\Users\adisis\Downloads\D7024E\d7024e> docker compose -f labs\docker-compose.yml up -d --scale knode=50
PS C:\Users\adisis\Downloads\D7024E\d7024e> docker compose -f labs\docker-compose.yml ps
```

---

## 4) Interacting with the network

### Attach to the seed’s interactive CLI

The seed has a TTY so you can type `put`/`get`:

```powershell
PS ...\labs> docker attach $(docker compose ps -q seed)

# In the attached CLI:
put hello container world
# -> prints a 40-hex key (SHA-1)
# You can now 'get <key>' from any node (or watch logs for FIND_VALUE hits)
```

> Tip: To exit the attach without killing the seed, use Ctrl+P, Ctrl+Q (Docker detach sequence).

### Exec a shell inside a node (when you don’t have TTY on knodes)

```powershell
# pick any knode container ID
PS ...\labs> docker compose ps
# exec a shell
PS ...\labs> docker compose exec knode sh
# (If you gave knode stdin_open/tty in compose, you can attach like the seed.)
```

### Scale up/down

```powershell
# Scale to 100 nodes
PS ...\labs> docker compose up -d --scale knode=100

# Scale down to 20 nodes
PS ...\labs> docker compose up -d --scale knode=20
```

### Stop / Start / Tear down

```powershell
# Stop containers (keep them)
PS ...\labs> docker compose stop

# Start again
PS ...\labs> docker compose start

# Remove everything (containers + network)
PS ...\labs> docker compose down

# Remove including anonymous volumes (if any)
PS ...\labs> docker compose down -v
```

---

## 5) How Docker networking maps to Kademlia

* **Bridge network (`kadnet`)**: All containers join the same private L2 network. UDP between containers works out of the box.
* **Service discovery**: Containers resolve `seed` to its container IP via Docker DNS. That’s how nodes discover the bootstrap address.
* **Announce vs Bind**: The entrypoint **announces** `<containerIP>:9000` via `-addr`. Nodes store and contact that address. Binding to that IP (or `0.0.0.0`) is fine; what matters is that the announced address is routable by peers.
* **Port exposure**: We use `expose: 9000/udp` (intra-network). There’s no need to publish ports to the host unless you want to hit a node from outside the Docker network.

---

## 6) Verifying behavior

* **Bootstrapping**: After bring-up, `seed` logs should show a flurry of `PING` and `FIND_NODE` as replicas bootstrap and warm routing tables.
* **Put/Get**:

  * `put` on the seed prints a key. `get <key>` on any node should find it. With our fixes, either:

    * the lookup reaches the holder (and **path caching** stores closer), or
    * the **republisher** migrates values to the current K-closest nodes over time.
* **Routing eviction**: Buckets ping LRU on overflow; dead entries are evicted; alive entries stay (newcomer goes to replacement cache). Under churn, routing tables stay fresh.

---

## 7) Troubleshooting (common pitfalls)

**A) “services.seed.build must be a string”**
Your YAML indentation was wrong. Use:

```yaml
build:
  context: ..
  dockerfile: labs/Dockerfile
```

**B) `COPY go.mod … not found` / `go.sum not found`**
We intentionally build with `GO111MODULE=off` and **don’t** copy `go.mod`. The Dockerfile copies the **entire repo** and mirrors `labs/kademlia` into GOPATH so the import `d7024e/kademlia` resolves.

**C) `cannot find package "d7024e/kademlia"`**
That’s exactly why we:

* set `GO111MODULE=off`, and
* copy `labs/kademlia` → `/go/src/d7024e/kademlia` in the Dockerfile.

**D) Entrypoint mismatch**
Our image `ENTRYPOINT` is `/app/entrypoint.sh`. Compose `command` is either `["seed"]` or `["node"]`; those are argv to the entrypoint (not absolute script paths).

**E) Windows line endings in `entrypoint.sh`**
The Dockerfile runs: `sed -i 's/\r$//' /app/entrypoint.sh`. Keep it; CRLF scripts break in Alpine.

**F) “no configuration file provided: not found”**
Run `docker compose` from **the folder that contains the compose file**, or use `-f labs\docker-compose.yml` from the repo root.

---

## 8) Performance & tuning tips

* **Alpha & timeouts**: If you see timeouts under heavy load, reduce α (parallel RPC fanout) or increase `timeoutRPC` in code.
* **Republish interval**: For demos, shorten to 1–2 minutes to watch values migrate quickly. For production semantics, use tens of minutes/hours.
* **Host UDP buffers**: For very large networks, you may need to increase host UDP receive buffers (sysctls on Linux hosts).

---

## 9) Optional: run just a few nodes locally (no compose)

Build once, then start 1–3 containers manually:

```powershell
# Build an image from labs/Dockerfile with repo root as context
PS ...\d7024e> docker build -f labs\Dockerfile -t d7024e-node .

# Create a bridge network
PS ...\d7024e> docker network create kadnet

# Seed
PS ...\d7024e> docker run -d --name seed --network kadnet d7024e-node seed

# A node bootstrapping to seed
PS ...\d7024e> docker run -d --name node1 --network kadnet -e BOOTSTRAP_HOST=seed d7024e-node node
```

---

## 10) Evolving to Go modules later (optional)

If you later add a `go.mod` at repo root and change imports to a module path (e.g., `module github.com/you/d7024e`), you can:

* remove `GO111MODULE=off`,
* `COPY go.mod go.sum ./` then `RUN go mod download`,
* remove the GOPATH mirror step, and
* build with `go build ./labs/kademlia/cmd/cli`.

Until then, the GOPATH approach in this Dockerfile is the simplest path that matches your current imports.

---

## 11) One-page quickstart (TL;DR)

```powershell
# from d7024e\labs
docker compose build --no-cache
docker compose up -d --scale knode=50
docker compose ps
docker compose logs -f seed
docker attach $(docker compose ps -q seed)
# then in the attached CLI:
put hello 50-node net
# Ctrl+P, Ctrl+Q to detach
```
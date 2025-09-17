## Quick % for everything

```bash
# from your module root
go test ./... -cover
```

## Produce a coverage file (multi-package, cross-package)

```bash
go test ./... \
  -covermode=atomic \
  -coverpkg=./... \
  -coverprofile=coverage.out
```

* `-covermode=atomic` plays nice with concurrency (and `-race`).
* `-coverpkg=./...` instruments all your packages, not just the one with tests.

## Human-readable summaries

```bash
go tool cover -func=coverage.out
```

Shows per-func and total coverage.

## HTML heatmap (line coverage)

```bash
go tool cover -html=coverage.out -o coverage.html
# open coverage.html in a browser
```

## While running a single package

```bash
go test -cover ./kademlia
```

## With the race detector

```bash
go test ./... -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out
```

---

## (Optional) Runtime coverage for binaries (Go ≥1.20)

If you want coverage from integration runs (e.g., your UDP nodes spun up without `go test`):

```bash
# 1) Build an instrumented binary
go build -cover -coverpkg=./... -o bin/node ./cmd/node

# 2) Run it with coverage output enabled
GOCOVERDIR=./covdata ./bin/node ...   # run your scenario; cov files land in ./covdata

# 3) Convert to a standard profile, then view
go tool covdata textfmt -i=./covdata -o runtime.out
go tool cover -func=runtime.out
go tool cover -html=runtime.out -o runtime.html
```

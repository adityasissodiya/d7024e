> **Always run from the package directory:**
> `C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia`

---

# Run all tests

```powershell
cd C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia

# fresh run, no cache, longer timeout to avoid UDP flakiness
go test . -count=1 -timeout=120s -v
```

---

# Quick coverage summary (on screen)

```powershell
go test . -count=1 -timeout=120s -cover
# -> prints: "coverage: NN.N% of statements"
```

---

# Generate a coverage profile + detailed report

```powershell
# 1) create the profile (atomic is safest with goroutines)
go test . -count=1 -timeout=120s -covermode=atomic -coverpkg=./... -coverprofile=coverage.out

# 2) per-function breakdown in the console
go tool cover -func=coverage.out

# 3) HTML report (open in a browser)
go tool cover -html=coverage.out -o coverage.html
Invoke-Item .\coverage.html     # or: Start-Process .\coverage.html
```

> If you see “file not found” for `coverage.out`, you either ran `go test` in a different folder or used a path that didn’t get created. Stick to the commands above (no variables) and run them **from `labs\kademlia`**.

---

# (Optional) Add the race detector

On Windows you need CGO enabled; if you have a C toolchain installed:

```powershell
$env:CGO_ENABLED=1
go test . -count=1 -timeout=120s -race -covermode=atomic -coverpkg=./... -coverprofile=coverage.out
```

If that errors about CGO/toolchain, just drop `-race`.

---

# Run only a subset (handy while iterating)

```powershell
# Only M1 tests
go test . -count=1 -timeout=120s -run '^Test(?!M2_|M3_)'

# Only M2 tests
go test . -count=1 -timeout=120s -run '^TestM2_'

# Only M3 tests
go test . -count=1 -timeout=120s -run '^TestM3_'
```

(You can still add `-cover` or `-coverprofile=…` to any of these.)

---

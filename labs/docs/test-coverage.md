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

# Windows: Reliable Go Coverage (PowerShell-proof)

> TL;DR: PowerShell sometimes eats `-coverprofile`. Use the `cmd.exe` one-liner below or write the profile to a short local name. Then use the **space** form for `go tool cover`.

## Run all tests + write coverage profile (robust on Windows)

```powershell
# Run from the package dir:
# C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia

# Absolute path via cmd.exe (works even when PowerShell mangles args)
$path = "C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia\coverage.out"
cmd /c "go test . -count=1 -timeout=120s -covermode=atomic -coverpkg=./... -coverprofile=""$path"""
```

## Coverage reports

```powershell
# Quick % in console (no file)
go test . -count=1 -timeout=120s -cover

# Per-function breakdown (use SPACE form, not =)
go tool cover -func "$path"

# HTML report
go tool cover -html "$path" -o "C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia\coverage.html"
Invoke-Item "C:\Users\adisis\Downloads\D7024E\d7024e\labs\kademlia\coverage.html"
```

## While iterating (subset test runs)

```powershell
# Only M1 tests
go test . -count=1 -timeout=120s -run '^Test(?!M2_|M3_)'

# Only M2 tests
go test . -count=1 -timeout=120s -run '^TestM2_'

# Only M3 tests
go test . -count=1 -timeout=120s -run '^TestM3_'
```

## (Optional) Race detector

```powershell
# Requires CGO + C toolchain on Windows. If it errors, drop -race.
$env:CGO_ENABLED=1
cmd /c "go test . -count=1 -timeout=120s -race -covermode=atomic -coverpkg=./... -coverprofile=""$path"""
```

## If you want the race detector, install a C toolchain

Fastest path is MSYS2 + mingw-w64. Do this once, then re-run with `-race`.

```powershell
# 1) Install MSYS2 (once)
winget install MSYS2.MSYS2          # if winget is available
# or download MSYS2 manually and install to C:\msys64

# 2) In "MSYS2 MSYS" shell, install mingw-w64 gcc (UCRT64 is best with modern Go)
pacman -S --needed --noconfirm base-devel
pacman -S --needed --noconfirm mingw-w64-ucrt-x86_64-gcc

# 3) Back in PowerShell: put the mingw bin on PATH for this session
$env:PATH="C:\msys64\ucrt64\bin;$env:PATH"
$env:CC="gcc"
$env:CGO_ENABLED="1"
gcc --version      # sanity check
go env CC          # should show gcc
go env CGO_ENABLED # should be 1

# 4) Now -race works
cmd /c "go test . -count=1 -timeout=120s -race -covermode=atomic -coverpkg=./... -coverprofile=""$path"""
go tool cover -func "$path"
```

## Troubleshooting (don’t skip)

* If `go tool cover` says “file not found”, you didn’t actually create it. **Always** use the same absolute `$path` for both creation and reading.
* Avoid PowerShell line continuations/backticks; they’re fragile. Keep the `go test ... -coverprofile=...` on **one line** or use the `cmd /c` form above.
* Use the **space** form with `go tool cover` (`-func <file>`, `-html <file>`).
* Sanity checks:

  ```powershell
  Test-Path "$path"
  Get-Item "$path" | Format-List Length, FullName
  go tool -n cover
  ```

---


Here are the manual steps. They work for any module that uses otelc; examples are from this repo.

### 1. Find the commit behind the otelc release tag
The otelc `pkg` and `instrumentation/...` modules have no version tags of their own, so you pin them to the commit that the release tag points at.

```sh
go mod download -json go.opentelemetry.io/otelc@v1.2.0
```
In the output, `Origin.Hash` is the commit, e.g. `70b4fe80ffb5032e6cdb79f543da5069ee4133b5`. The first 12 characters (`70b4fe80ffb5`) are enough.

To get the full version string Go will write into go.mod:
```sh
go list -m go.opentelemetry.io/otelc/pkg@70b4fe80ffb5
# -> go.opentelemetry.io/otelc/pkg v0.0.0-20261009154624-70b4fe80ffb5
```

### 2. Update your shared instrumentation module first
Do this one first. If it still requires a newer `otelc/pkg`, Go raises the version again in the apps, or drops your module from them.

```sh
cd instrumentation
go get go.opentelemetry.io/otelc/pkg@70b4fe80ffb5 go.opentelemetry.io/otelc/pkg/runtime@70b4fe80ffb5
go mod tidy
go test ./...
```

### 3. Clean each app module (`demo/app/mcp/server`, `demo/app/mcp/client`)
**a. Remove the local replace lines.** In go.mod, delete every line like:
```
replace go.opentelemetry.io/otelc/... => D:\...\.otelc-build\...
```
Keep your own relative replace (`ps-otel/instrumentation => ../../../../instrumentation`).

You can delete them by hand in the editor, or with a command:
```sh
# Git Bash
sed -i '/\.otelc-build/d' go.mod
```
```powershell
# PowerShell
(Get-Content go.mod) | Where-Object { $_ -notmatch '\.otelc-build' } | Set-Content -Encoding utf8 go.mod
```

**b. Pin every `go.opentelemetry.io/otelc/pkg...` and `.../instrumentation...` require to the release commit.** Either find-and-replace the old version string (e.g. `v0.0.0-20261021153517-5dd2074874c9`) with `v0.0.0-20261009154624-70b4fe80ffb5` in go.mod, or use `go get` for each module:
```sh
go get go.opentelemetry.io/otelc/instrumentation/net/http/server@70b4fe80ffb5 \
       go.opentelemetry.io/otelc/instrumentation/log@70b4fe80ffb5   # ...and the rest
```
Leave `go.opentelemetry.io/otelc v1.2.0` as it is; that one is a real tag.

**c. Tidy:**
```sh
go mod tidy
```

### 4. Check it
```sh
go tool otelc version                 # should print v1.2.0
go tool otelc go build -o server.exe .
grep -c otelc-build go.mod            # should print 0
```
- If you want to confirm the rules were applied, look in `.otelc-build/matched.json` for both the `otelc/instrumentation/...` and `ps-otel/instrumentation/...` paths.
- Optionally run `go tool otelc pin` once more and confirm go.mod still has no `.otelc-build` lines.

### 5. Commit
Commit `go.mod`, `go.sum` and `otel.instrumentation.go` for each module. Never commit `.otelc-build/`; your `.gitignore` already excludes it.

### Upgrading otelc later
1. Get the new tag's commit, as in step 1, with `go mod download -json go.opentelemetry.io/otelc@vX.Y.Z`.
2. Update the tool: `go get go.opentelemetry.io/otelc@vX.Y.Z`.
3. Repeat steps 2–4 with the new commit hash. Keep the tool version and all the submodule pins on the same release.

### Rules to keep in mind
- **Keep `otel.instrumentation.go` committed.** `pin` only writes the local `.otelc-build` paths when that file is missing.
- **Always check go.mod after `pin` on a new module.** If you see `.otelc-build` lines, delete them as in step 3a before committing.
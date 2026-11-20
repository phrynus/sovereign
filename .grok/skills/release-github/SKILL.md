---
name: release-github
description: Push Sovereign to GitHub, create git tag, publish GitHub Release with server binary. Use for release, tag, push to GitHub, or /release-github.
license: MIT
compatibility: grok
metadata:
  audience: sovereign-devs
  workflow: deploy
---

## When to use

Ship a version to GitHub with tag + Release asset (`sovereign-server.exe`).

## Pre-release checklist

| Step | Command / action |
|------|------------------|
| 1 | `go test ./...` |
| 2 | `powershell -File scripts\build-embedded.ps1` (full frontend + Go build) |
| 3 | `go build -o sovereign-server.exe ./cmd/server/` |
| 4 | Update `README.md` / `README.zh.md` version line if needed |

## Git push + tag

```powershell
git add -A
git commit -m "vX.Y.Z: <summary>"
git push origin main

git tag -a vX.Y.Z -m "vX.Y.Z: <summary>"
git push origin vX.Y.Z
```

## GitHub Release (gh CLI)

```powershell
$env:Path += ";C:\Program Files\GitHub CLI"
gh auth login   # once, if not authenticated

gh release create vX.Y.Z `
  --title "vX.Y.Z" `
  --notes "## Highlights`n- ..." `
  sovereign-server.exe
```

## GitHub Release (API fallback)

If `gh` unavailable, use GitHub API `POST /repos/r0si/sovereign/releases` then upload asset to `uploads.github.com`.

## Release notes template

```markdown
## vX.Y.Z

### UI
- ...

### Implant / C2
- ...

### Install
go build -o sovereign-server.exe ./cmd/server/
.\sovereign-server.exe -config config.yaml
```

## Verify

- Tag visible at https://github.com/r0si/sovereign/tags
- Release page has `sovereign-server.exe` download
- CI badge green on `main`
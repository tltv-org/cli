# AGENTS.md -- TLTV CLI

Command-line tool for the TLTV Federation Protocol. Single Go binary, zero external dependencies.

## Repo Structure

```
main.go             Entry point, command dispatch, identity/document/URI commands
base58.go           Base58 encode/decode (Bitcoin alphabet, big.Int based)
identity.go         Channel ID: make, parse, validate; version prefix 0x1433
signing.go          Canonical JSON (RFC 8785 via json.Marshal), Ed25519 sign/verify
uri.go              tltv:// URI parse and format (no net/url -- preserves channel ID case)
client.go           HTTP client for TLTV nodes (node info, metadata, guide, peers, stream)
network.go          Network command implementations (node, fetch, guide, peers, stream, crawl)
vanity.go           Multi-threaded vanity miner (goroutines + crypto/rand)
output.go           Terminal output helpers (colors, tables, field display)
signal.go           OS signal handling (SIGINT/SIGTERM)
main_test.go        Tests against all 7 protocol test vector suites (C1-C7)
Makefile            Build targets: build, install, test, release, clean
```

## Key Design Decisions

- **All `package main`** -- No internal packages. The CLI is simple enough for a flat structure. All files compile into one binary.
- **`json.Decoder.UseNumber()`** -- Used when reading documents for signing/verification to preserve exact integer representation (avoids float64 round-trip issues with canonical JSON).
- **`json.Encoder.SetEscapeHTML(false)`** -- Used for document output so `<` and `>` aren't escaped to `\u003c`/`\u003e`.
- **No `net/url` for URI parsing** -- The `tltv://` URI parser is hand-written to avoid `net/url` lowercasing the host component, which would corrupt base58 channel IDs.
- **Localhost detection** -- Network commands auto-detect `localhost`/`127.0.0.1`/`[::1]` and default to HTTP instead of HTTPS for local development.
- **Version prefix encoding constraint** -- The `0x1433` prefix constrains which base58 characters can appear at position 2 (after TV). Not all 58 characters are achievable there. The vanity miner documents this and suggests `--mode contains` as a fallback.

## Protocol Alignment

The implementation tracks the TLTV Federation Protocol v1.0 spec at `git.plutoniumtech.com/tltv/protocol`. Key spec sections:

| CLI feature | Spec section |
|---|---|
| Channel ID encoding | 2.1-2.3 |
| URI parsing | 3 |
| Canonical JSON | 4 |
| Metadata signing | 5, 7 |
| Guide documents | 6 |
| Protocol endpoints | 8 |
| Peer exchange | 11 |
| Migration | 5.14 |

## Testing

```bash
make test    # or: go test -v ./...
```

22 tests validate against all protocol test vectors:
- C1: identity encoding, C2: signing, C3: complete document, C4: URI parsing, C5: guide, C6: invalid inputs, C7: migration
- Plus base58 edge cases, canonical JSON ordering, signature hex verification

## Building

```bash
make build              # current platform -> ./tltv
make release            # all platforms -> dist/
make install            # -> $GOPATH/bin/tltv
```

Cross-compilation: `GOOS=<os> GOARCH=<arch> go build -o tltv .`

Version injection: `-ldflags "-X main.version=X.Y.Z"`

## Git

- Author: `Philo Farnsworth <farnsworth27@protonmail.com>`
- Do not include Co-Authored-By lines in commit messages
- `origin` -- Forgejo (git.plutoniumtech.com/tltv/cli)
- `github` -- GitHub (github.com/tltv-org/cli). Public release. Squashed history.

### Branching

- **`main`** -- Release branch. Only receives merges from `dev`. Tags (`v*`) trigger GitHub Actions release builds.
- **`dev`** -- Integration branch. Ongoing work lands here. Feature branches merge to `dev` via PR.
- **Feature branches** -- Branch off `dev`, merge back to `dev`. Name: `feature/<name>` or just descriptive (`vanity-optimization`, `add-resolve-command`).
- **Release flow**: `dev` -> PR to `main` -> merge -> tag `vX.Y.Z` on `main` -> Actions builds binaries.

### CI

- `.github/workflows/ci.yml` -- Runs build + tests on push to `main`/`dev` and on PRs.
- `.github/workflows/release.yml` -- Cross-compiles and creates GitHub Release when a `v*` tag is pushed to `main`.

## Common Tasks

### Adding a new command

1. Add the command function in the appropriate file (network.go for network commands, main.go for everything else)
2. Register it in the `switch` statement in `main()` 
3. Add it to the `usage()` help text
4. Add it to README.md

### Updating for a new protocol version

1. Update test vectors in `main_test.go` to match new `test-vectors/` files
2. Update `VersionPrefix` in `identity.go` if the key format changes
3. Update protocol version in `cmdVersion()` and help text
4. Run `make test` to verify

### Adding a new flag to a command

Use `flag.NewFlagSet` for the command. Flags must come before positional arguments (Go `flag` package convention).

### GitHub Release Process

GitHub gets a squashed single-commit release. Forgejo keeps the full history.

1. Finalize all changes on `dev`, merge to `main`, push to `origin` (Forgejo).
2. Run tests: `make test`
3. Create an orphan branch for GitHub:
   ```bash
   git checkout --orphan github-release
   git reset
   git add .gitignore .github/ LICENSE README.md Makefile go.mod \
       *.go
   ```
4. Do NOT include: AGENTS.md or anything not meant for public.
5. Commit with a clean message:
   ```bash
   git commit -m "TLTV CLI vX.Y.Z

   <one paragraph summary>"
   ```
6. Push and tag:
   ```bash
   git push github github-release:main --force
   git push github <commit-hash>:refs/tags/vX.Y.Z
   ```
7. Switch back: `git checkout -f dev`

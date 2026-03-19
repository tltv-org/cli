# AGENTS.md -- TLTV CLI

Command-line tool for the TLTV Federation Protocol. Single Go binary, zero external dependencies.

## Repo Structure

```
main.go             Entry point, command dispatch, identity/document/URI/completion commands
base58.go           Base58 encode/decode (Bitcoin alphabet, big.Int based)
identity.go         Channel ID: make, parse, validate; version prefix 0x1433
signing.go          JCS canonical JSON (RFC 8785), Ed25519 sign/verify, strict validation
uri.go              tltv:// URI parse and format (no net/url -- preserves channel ID case)
client.go           HTTP client, SSRF-safe client, hint validation, local address detection
network.go          Network command implementations (resolve, node, fetch, guide, peers, stream, crawl)
update.go           Self-update command (GitHub API, archive extraction, atomic binary replace)
vanity.go           Multi-threaded vanity miner (goroutines + crypto/rand, pos-2 constraint detection)
output.go           Terminal output helpers (colors, tables, field display)
signal.go           OS signal handling (SIGINT/SIGTERM)
install.sh          Curl one-liner installer (detects OS/arch, downloads latest from GitHub API)
main_test.go        55 tests against all 7 protocol test vector suites (C1-C7) + security/edge cases
Makefile            Build targets: build, install, test, release, clean (CGO_ENABLED=0)
```

## Key Design Decisions

- **All `package main`** -- No internal packages. The CLI is simple enough for a flat structure. All files compile into one binary.
- **`json.Decoder.UseNumber()`** -- Used when reading documents for signing/verification to preserve exact integer representation (avoids float64 round-trip issues with canonical JSON).
- **`json.Encoder.SetEscapeHTML(false)`** -- Used for document output so `<` and `>` aren't escaped to `\u003c`/`\u003e`.
- **Real JCS canonical JSON** -- `canonicalJSON()` is a hand-written RFC 8785 serializer (`jcsSerialize`, `jcsWriteString`, `jcsFormatNumber`, `es6FloatString`). Does NOT use `json.Marshal`. Key differences from Go's stdlib: `<`, `>`, `&`, U+2028, U+2029 are literal (not `\uXXXX`-escaped); numbers use ES6 `Number.prototype.toString()` formatting. Keys sorted lexicographically (UTF-16 code unit order, equivalent to byte order for ASCII/BMP).
- **No `net/url` for URI parsing** -- The `tltv://` URI parser is hand-written to avoid `net/url` lowercasing the host component, which would corrupt base58 channel IDs.
- **Localhost detection** -- Network commands auto-detect `localhost`/`127.0.0.1`/`[::1]` and default to HTTP instead of HTTPS for local development. Uses `isLocalAddress()` which also covers RFC 1918, link-local, CGN (100.64.0.0/10), and unspecified (0.0.0.0, ::) ranges.
- **SSRF-safe client** -- `newSSRFSafeClient()` uses a custom `DialContext` (`ssrfSafeDialContext`) that resolves DNS, checks all resolved IPs against `isLocalAddress()` before connecting, and connects directly to the resolved IP to prevent TOCTOU. Used by `resolve` and `crawl` for untrusted hints. `validateHint()` rejects hints containing scheme/userinfo/path/query/fragment. `normalizeHint()` validates and adds default port.
- **Local address filtering** -- `resolve` and `crawl` skip hints pointing to private/loopback/link-local/CGN/unspecified addresses unless `--local` is set (spec section 3.1 SSRF protection). Defense is layered: string-level check via `isLocalAddress()` + DNS-level check at connection time via SSRF-safe dialer. Direct commands (`fetch`, `guide`, `node`) are not filtered since the user explicitly chose the target.
- **Version prefix encoding constraint** -- The `0x1433` prefix constrains which base58 characters can appear at position 2 (after TV). Not all 58 characters are achievable there. The vanity miner documents this and suggests `--mode contains` as a fallback.
- **Strict verification** -- `verifyDocument` and `verifyMigration` check protocol version (`v` must be 1), identity binding, future timestamps, and signature. `verifyMigration` additionally validates that `to` is a valid channel ID different from `from`. `fetch` and `guide` commands exit non-zero when verification fails (both human and JSON output modes). `checkTimestamps()` rejects malformed/wrongly-typed `seq`, `updated`, `migrated` instead of silently ignoring parse failures.
- **Document size limit** -- `readDocument` enforces the 64 KB limit from spec section 5.6 using `io.LimitReader`. Also rejects trailing data after the JSON document (concatenated JSON, garbage bytes).
- **Timestamp format validation** -- `cmdSign` validates `updated`, `migrated`, guide `from`/`until`, and guide entry `start`/`end` timestamps match the spec format (`YYYY-MM-DDTHH:MM:SSZ`) before signing. Uses roundtrip check to reject fractional seconds.
- **Migration chain following** -- `resolve` follows migration documents up to 5 hops (spec section 5.14), verifying each hop. Detects loops. Fails clearly with non-zero exit on broken chains (fetch failure, verification failure, missing `to`, loop, exceeded hops) instead of returning stale data.
- **XMLTV output** -- `guide --xmltv` outputs XMLTV XML for IPTV compatibility (spec section 6.6).
- **Static binary** -- All build paths (`make build`, `make release`, CI, release workflow) use `CGO_ENABLED=0` to produce fully static binaries with no libc dependency. Verified with `ldd` on Linux.
- **Unknown flag rejection** -- Unknown top-level global flags (e.g. `tltv --bogus`) error immediately with usage help instead of being silently ignored.
- **URI format uses `@` syntax** -- `formatTLTVUri` uses `@` for the first hint (spec-preferred) and `?via=` for additional hints. Single hint: `tltv://id@host:port`. Multiple: `tltv://id@host1:port?via=host2:port`.

## Protocol Alignment

The implementation tracks the TLTV Federation Protocol v1.0 spec at `git.plutoniumtech.com/tltv/protocol`. Key spec sections:

| CLI feature | Spec section |
|---|---|
| Channel ID encoding | 2.1-2.3 |
| URI parsing | 3 |
| Canonical JSON | 4 |
| Metadata signing | 5, 7 |
| Guide documents | 6 |
| Future timestamp rejection | 7.2 |
| Protocol endpoints | 8 |
| URI resolution procedure | 3.1, 8 |
| SSRF protection (local addr) | 3.1 |
| Document size limit | 5.6 |
| Timestamp format | 6.4 |
| XMLTV compatibility | 6.6 |
| Peer exchange | 11 |
| Migration + chain following | 5.14 |

## Testing

```bash
make test    # or: go test -v ./...
```

55 unit tests + 7 integration tests validate against all protocol test vectors:
- C1: identity encoding, C2: signing, C3: complete document, C4: URI parsing, C5: guide, C6: invalid inputs, C7: migration
- Plus base58 edge cases, canonical JSON ordering, signature hex verification
- URI format/parse roundtrip, vanity pos-2 feasibility, future timestamp rejection
- Protocol version validation, migration identity binding mismatch, migration `to` field validation
- Future `updated` and `migrated` timestamp rejection (independent of `seq`)
- Document size limit enforcement, timestamp format validation (roundtrip check)
- Local/private address detection (loopback, RFC 1918, link-local, CGN, unspecified)
- IPv6 bracketed hint parsing, XMLTV time conversion
- JCS canonical JSON: special chars, Unicode separators, control chars, number formatting, nested, sign stability
- SSRF hint validation: URL rejection, userinfo, path, query, fragment, normalizeHint
- Strict validation: seq type/format, timestamp type/format, trailing JSON, guide entry timestamps

### Integration tests

7 integration tests exercise the full network stack against a live TLTV node (node info, fetch+verify, guide+verify, peers, resolve end-to-end, SSRF-safe client behavior, crawl JSON structure). They are gated by `TLTV_TEST_NODE=host:port` and skip automatically when unset or unreachable.

```bash
TLTV_TEST_NODE=host:port go test -v -run TestIntegration ./...
```

**TODO:** Stand up a permanent test node so integration tests can run in CI.

## Building

```bash
make build              # current platform -> ./tltv
make release            # all platforms -> dist/
make install            # -> $GOPATH/bin/tltv
```

Cross-compilation: `GOOS=<os> GOARCH=<arch> CGO_ENABLED=0 go build -o tltv .`

Version injection: `-ldflags "-X main.version=X.Y.Z"`

## Git

- Author: `Philo Farnsworth <farnsworth27@protonmail.com>`
- **All commits must be authored as Philo.** Do not commit as the agent identity. The GitHub release squash ensures only Philo appears in public history, but Forgejo history should also use the correct author.
- **Both author AND committer must be Philo.** `--author` alone is not enough -- GitHub displays the committer too. Use `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL` env vars:
  ```bash
  GIT_COMMITTER_NAME="Philo Farnsworth" GIT_COMMITTER_EMAIL="farnsworth27@protonmail.com" \
      git commit --author="Philo Farnsworth <farnsworth27@protonmail.com>" -m "message"
  ```
- Do not include Co-Authored-By lines in commit messages
- `origin` -- Forgejo (git.plutoniumtech.com/tltv/cli). Full history. Push here normally.
- `github` -- GitHub (github.com/tltv-org/cli). Public release. Curated history.
- `gh-push` -- Local branch tracking `github/main`. Used to stage commits for GitHub.

### Branching

- **`main`** -- Release branch. Only receives merges from `dev`. Tags (`v*`) trigger release builds.
- **`dev`** -- Integration branch. Feature branches merge to `dev` via PR. Direct commits OK for small fixes.
- **`gh-push`** -- Local branch tracking `github/main`. Used to stage curated commits for GitHub.
- **Feature branches** -- Branch off `dev`, merge back to `dev`. Name: `feature/<name>` or just descriptive.
- **Release flow**: commit to `dev` -> merge `dev` to `main` -> tag `vX.Y.Z` on `main` -> push. Always commit to `dev` first, never directly to `main`.

### CI

- `.github/workflows/ci.yml` -- Runs build + tests on push to `main`/`dev` and on PRs.
- `.github/workflows/release.yml` -- Cross-compiles, creates release, uploads assets when a `v*` tag is pushed. Runs on both Forgejo and GitHub. Produces `.tar.gz` for Linux/macOS/FreeBSD and `.zip` for Windows. Deletes stale releases from previous failed runs before creating a new one.

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

Use `flag.NewFlagSet` for the command. Flags must come before positional arguments (Go `flag` package convention). Exception: `cmdFormat` manually extracts `--hint` and `--token` from args to support repeatable flags and flags after positional arguments.

### Version Bumps

- New features = minor bump (v1.1.0 -> v1.2.0)
- Bug fixes = patch bump (v1.1.0 -> v1.1.1)
- CI/workflow-only fixes do NOT get new versions. Re-tag the same version after fixing.

### Forgejo Release Process

Forgejo is the primary CI. Release notes are not needed here (private server).

1. Commit to `dev`, merge `dev` to `main` (never commit directly to `main`).
2. Tag on `main`: `git tag vX.Y.Z`
3. Push: `git push origin main dev --tags`
4. The release workflow builds archives, creates a Forgejo release, and uploads assets.
5. If CI fails and a stale release was created, the workflow auto-deletes it on retry.
   To manually re-trigger: delete the tag remotely, re-tag, and push.
   ```bash
   git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z
   git tag vX.Y.Z && git push origin --tags
   ```

### GitHub Release Process

GitHub gets curated commits (no Forgejo history). Changes are overlaid onto GitHub's
existing history as a single clean commit per release.

1. Finalize all changes on `main` and push to `origin` (Forgejo).
2. Run tests: `make test`
3. Overlay current files onto GitHub's history:
   ```bash
   git checkout gh-push
   git checkout main -- .gitignore .github/ LICENSE README.md Makefile go.mod *.go install.sh
   ```
   **Note:** `*.go` may not glob correctly in `git checkout`. Explicitly list new `.go`
   files (e.g. `update.go`) if they don't appear in `git status` after the checkout.
4. Do NOT include: AGENTS.md or anything not meant for public.
   ```bash
   git reset HEAD AGENTS.md 2>/dev/null
   git checkout -- AGENTS.md 2>/dev/null
   ```
5. Commit with author set to Philo (amend the previous unpushed commit if adding to it):
   ```bash
   GIT_AUTHOR_NAME="Philo Farnsworth" GIT_AUTHOR_EMAIL="farnsworth27@protonmail.com" \
   GIT_COMMITTER_NAME="Philo Farnsworth" GIT_COMMITTER_EMAIL="farnsworth27@protonmail.com" \
   git commit -m "<description of changes>"
   ```
6. Push code and tag:
   ```bash
   git push github gh-push:main
   git push github <commit-hash>:refs/tags/vX.Y.Z
   ```
7. Wait for GitHub Actions to create the release and upload assets.
8. Edit the release with proper notes (the CI creates it with a bare body):
   ```bash
   GH_TOKEN=<token> gh release edit vX.Y.Z --repo tltv-org/cli --notes "<markdown notes>"
   ```
   Or use `gh release edit --notes-file` with a file. Write the notes manually --
   GitHub's `--generate-notes` produces poor output with single squashed commits.
9. Switch back:
   ```bash
   git checkout dev
   ```

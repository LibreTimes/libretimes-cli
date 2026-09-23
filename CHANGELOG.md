# Changelog

All notable changes to `lt` are recorded here. This project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

First implementation. `lt` replaces the Python tool dispatcher that lived
inside the LibreTimes monorepo as `agent/app/cli.py`; the tool layer it sat on
stays there, serving MCP.

### Added

- `lt auth login` — OAuth 2.1 authorization code with PKCE, catching the
  redirect on a loopback port (RFC 8252, RFC 7636).
- `lt auth login --device` — the device authorization grant (RFC 8628), for
  containers, SSH sessions, CI runners, and anywhere else without a browser.
- `lt auth login --force` — sends `prompt=login` so Keycloak starts a new
  session instead of reusing the browser's SSO cookie. The only recovery from
  a server-side sign-out.
- `LT_TIMEOUT_SECONDS` scales every HTTP phase budget.
- `LT_SKIP_CHECKSUM=1` overrides install.sh's mandatory checksum verification.
- `lt auth logout`, `lt auth status`, `lt auth token`.
- `lt whoami`.
- `lt publish` — reconciles markdown files as publications by `import_key`,
  find-then-update, with `--dry-run` and `--yes`.
- `contributors` frontmatter on `lt publish` — byline credits by handle or
  profile_id, with a contributor role (`lecturer` for course notes). Sent with
  the create; on a re-run, the byline is read and only missing names are
  added. Never removes a name or changes a role, and never re-asks a declined
  invitation. `--dry-run` reports `add`/`present` per name.
- `lt course publish` — reconciles a course manifest as a `course` collection by
  its `import_key`: creates or updates it, adds the listed lectures that are
  published (found by each lecture file's own `import_key`), and restores the
  manifest's order. Skips lectures that are unpublished, drafts or deleted;
  never removes an item. `--dry-run` and `--yes`. Needs the platform's
  collection `import_key` (#1040).
- `lt book publish` — reconciles a book manifest (a contents tree, one markdown
  file per chapter) as a publication of type `book` by its `import_key`:
  creates it as a draft, creates, updates and moves chapters to match the
  manifest (matched by `ref`, else by position), and publishes it once its
  chapters have text. A chapter the manifest no longer lists stops the run
  unless `--prune` deletes it. `--dry-run` walks the same placements and
  writes nothing. Needs the platform's authored books (#1080).
- `lt book publish` writes only the text in the manifest's `language`. It used
  to fall back to the book's own language, which since book translations
  would have written one language's chapters over another's; a language the
  book has no text in now fails with `no_expression`.
- `.localhost` host names resolve to loopback in every HTTP client (RFC 6761),
  as browsers and curl already do. The binary is built without cgo, so Go's
  resolver asked public DNS for them, and sign-in to a dev stack behind
  `*.libretimes.localhost` failed at the token exchange.
- `--json` on every command; fixed exit codes (0/1/2/3/4).
- `--env dev` targets the dev stack behind its proxy
  (`https://auth.libretimes.localhost`, `https://api.libretimes.localhost/libretimes/v1`),
  the hosts a proxied Keycloak and its BFFs accept, rather than the plain
  compose ports, where sign-in failed with `authentication_expired` (#1094).
  The plain ports stay reachable with `--auth-url` / `--api-url`.
- An untrusted TLS certificate is reported as such, with the fix for the dev
  CA, rather than as "could not reach".
- `--env {prod,dev}` resolves API base, Keycloak URL, realm and client id
  together.
- Single-file binaries for linux, darwin and windows on amd64 and arm64, all
  cross-compiled from one Linux runner.

### Fixed

- **The repository and module are `libretimes-cli`, not `lt`.** The binary,
  the archives and the Homebrew cask stay `lt` — descriptive repo, short
  command, the `gh`/`kubectl` convention. Caught before the first tag, because
  the module path is baked into every import.

- **`install.sh` installed unverified binaries.** Its own comment said
  verification was "not optional", but three paths skipped it — a missing
  `checksums.txt`, a missing entry for the archive (silently, with no warning
  at all), and no `sha256sum`/`shasum`. All three now abort;
  `LT_SKIP_CHECKSUM=1` is the deliberate override. The archive is matched as an
  exact field rather than a grep pattern, since its name contains dots.

- **The drift check could not fail, and would not have caught anything.** Two
  defects: `check ... | tee` reported *tee's* exit code under GitHub's default
  `bash -e` (no `pipefail`), so drift never failed the step; and the snapshot
  compared path objects, which hold only `$ref`s — a renamed field inside
  `ProfileMe` left every path byte-identical. Snapshots now resolve refs
  transitively (4 paths, 33 schemas) and report field-level changes.

- **`SESSION_REVOKED` and `ONBOARDING_REQUIRED` were never recognised.** The
  platform envelope puts the code at `error.detail.error_code` — an *object* —
  while `codeOf` only read `error.code`. Every discrimination fell through to
  the generic branch, so a revoked session got advice for an expired token. The
  onboarding fixtures used a string-shaped `detail` that does not ship, which
  is why they passed.

- **`--json` emitted no JSON on failure**, contradicting AGENTS.md. Failures
  now print `{"error":{"code","message"}}` on stderr, stdout stays empty, and
  per-file `publish` results carry `code` as a field instead of glued to the
  front of `message`.

- **HTTP timeouts were datacenter values on a laptop.** connect 2s / read 10s
  were copied from the platform's service-to-service convention and failed
  healthy requests: measured against production over a VPN, connect ranged
  1.0-3.7s and one response took 18.2s to first byte. Now 15s / 60s / 180s,
  each roughly 4x the worst observed, with `LT_TIMEOUT_SECONDS` to scale them.

- **`lt auth status` did not say who you were.** The cache holds one credential
  per deployment, so signing in as another account silently replaces the first
  while `publish` writes as whoever holds the token. Status now names the
  account.


- **`lt whoami` printed an empty `profile_id`.** `GET /profiles/me` returns the
  identity key as `id`, and the name parts as `given_name`/`family_name`; the
  client modelled `profile_id`, `first_name`, `last_name` and an `email` the
  response has never carried. The `--json` output kept its `profile_id` key —
  agents parse it, and a bare `id` would be ambiguous in a tool that also
  handles publication ids — so only the decode changed.

  Caught by running against the live API, not by the suite: the httptest
  fixtures asserted the same wrong field names, so the mock and the client
  agreed with each other and disagreed with the server. `contract.yml` did not
  catch it either — it checks that the four watched *paths* still exist, not
  that their schemas still match. Fixtures now use the real `ProfileMe` shape,
  and `internal/api/profiles_test.go` pins the decode, the emitted key, and the
  absence of anything from the auth boundary.

- **`lt publish` was unusable on Windows: no shell there expands a glob before
  handing it to the process.** `resolveFiles` now expands `*`/`?`/`[...]`
  itself, unconditionally rather than gated on `GOOS` — a no-op under a shell
  that already expanded one. `**` is refused outright (named, with the
  alternative) rather than silently matching one directory level, which is
  all `filepath.Glob` alone would give it. Found and fully catalogued in
  [`WINDOWS.md`](WINDOWS.md) by running the `windows/amd64` build on a real
  machine; six more of its nine findings are fixed alongside this one.

- **Credentials were never written where the README said they were, on
  Windows or (a second, previously unnoticed divergence) macOS.**
  `NewStore` now resolves via `os.UserConfigDir()` in place of a hand-written
  `os.UserHomeDir()` + `.config`, with the old path read as a one-release
  fallback so an existing login is not silently lost on upgrade.

- **The credential file was written non-atomically and tolerated no BOM.** A
  crash, a Ctrl-C, or an antivirus/indexer hold between truncate and write
  could leave `credentials.json` half-written, which `load` reads as an empty
  cache rather than an error — a silent "not signed in" with the refresh
  token gone. Now written to a temp file and `os.Rename`d over the target.
  Separately, any Windows tool that resaves the file (Notepad,
  `Set-Content -Encoding utf8`) adds a UTF-8 BOM, which `json.Unmarshal`
  rejected outright; `load` now strips it, matching `frontmatter.Split`.

- **Nothing in CI ever ran on Windows**, which is how the glob bug above
  shipped uncaught. The `test` job is now a `[ubuntu-24.04, windows-latest]`
  matrix.

### Changed from the Python implementation

- **Command shape.** `lt call create_publication --arg title=X --yes` becomes
  `lt publish`. Named subcommands with named flags, not a generic verb
  dispatcher.
- **`lt push` is now `lt publish`.** `push` implied a git-like mental model the
  reconcile semantics never matched.
- **Credentials are keyed by issuer and client.** Signing in to a dev stack no
  longer evicts a production login. The previous flat file is still read, so an
  existing login survives the upgrade.
- **Frontmatter is real YAML.** `tags: [a, b]` works; `tags: a, b` still works,
  because that is what the existing corpus uses.
- **The default `import_key` includes the parent directory** (`lt:ru/integrals`
  rather than `lt:integrals`), so a `ru/` and an `en/` tree of the same
  articles no longer collide and silently overwrite each other.

### Preserved deliberately

- Exit codes match the Python implementation, so existing scripts keep working.
- The `LIBRETIMES_*` environment variables are still honoured as overrides.
- Every 404 maps to one constant message with no interpolation. The platform
  answers 404 rather than 403 for a resource you may not see; varying the
  wording would make this client an existence oracle.
- Reconcile is find-then-update, never create-then-fallback-on-conflict, and a
  soft-deleted row is reported as `blocked` rather than resurrected.

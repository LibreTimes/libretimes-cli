# lt

The [LibreTimes](https://libretimes.io) command-line client.

```sh
lt auth login
lt publish content/*.md --dry-run
```

`lt` is a pure client of the LibreTimes public REST API. It holds no internal
credential, sets no privileged header, and reaches nothing a third party could
not reach with the same token. That is why it can be open source while the
platform is not.

## Install

**Shell installer** — Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/The-LibreTimes/libretimes-cli/main/install.sh | sh
```

**Homebrew** — macOS and Linux:

```sh
brew install The-LibreTimes/tap/lt
```

**From source** — needs Docker, not Go:

```sh
make build     # produces ./lt
```

**Windows** — take the `.zip` from
[Releases](https://github.com/The-LibreTimes/libretimes-cli/releases) and put `lt.exe`
somewhere on your `PATH`. There is no winget or Scoop package yet.

### A note for Windows users

The binaries are not yet Authenticode-signed. On a machine with **Smart App
Control** enabled — the default on a clean Windows 11 install — this blocks a
freshly downloaded `lt.exe` from running at all, with no per-file override;
disabling Smart App Control machine-wide is one-way (it requires a Windows
reinstall to turn back on) and not a fix worth reaching for. Build from source
with Docker instead (below) until this is signed. See
[WINDOWS.md](WINDOWS.md#2-the-windows-binary-is-unsigned-and-smart-app-control-refuses-to-run-it)
for the full picture.

`lt` writes UTF-8 unconditionally. Windows Terminal and PowerShell 7 render
that correctly by default; the legacy `conhost`/PowerShell 5.1 console does
not, and a title or `import_key` in Cyrillic or another non-ASCII script comes
out as mojibake there — cosmetic, not a data problem, since the bytes on disk
and over the wire are correct either way. Fixing the *display* would mean
calling `SetConsoleOutputCP(CP_UTF8)` on startup, which needs
`golang.org/x/sys/windows` — the first non-stdlib runtime dependency this
project would take for a problem most users, on a modern default terminal,
will never see. Left as documented rather than fixed, as a decision rather
than an oversight.

### A note for macOS users

Downloading the archive **through a browser** attaches Apple's
`com.apple.quarantine` attribute, and Gatekeeper will refuse to run an
unsigned binary that carries it. The installer and Homebrew both avoid this —
`curl` never sets the attribute, and Homebrew strips it. If you did download
through a browser:

```sh
xattr -d com.apple.quarantine lt
```

The binaries are not yet signed with an Apple Developer ID. They *are*
ad-hoc-signed by the Go linker, which is what the Apple Silicon kernel
requires to start a process at all; Gatekeeper is a separate, stricter check
that only applies to quarantined files.

### Verifying a download

The installer verifies the archive against the release `checksums.txt` and
**aborts if it cannot** — a missing checksums file, a missing entry, or no
`sha256sum`/`shasum` on the machine are all fatal rather than warnings. An
install that silently skipped the check would be indistinguishable from one
that passed it. `LT_SKIP_CHECKSUM=1` overrides, and says so loudly.

Every release ships `checksums.txt`, and the binaries carry a GitHub
build-provenance attestation:

```sh
gh attestation verify ./lt --repo The-LibreTimes/libretimes-cli
```

## Commands

```
lt auth login [--device]      sign in (--force to start a new session)
lt auth logout                forget cached credentials
lt auth status                show which deployments you are signed in to
lt auth token                 print a valid access token, for scripting
lt whoami                     the profile the current token belongs to
lt publish FILE...            reconcile markdown files as works
lt course publish MANIFEST... group published lecture notes into a course
lt book publish MANIFEST...   publish a book from a manifest and one file per chapter
lt version
```

Global flags: `--env {prod,dev}`, `--api-url`, `--auth-url`, `--realm`,
`--client-id`, `--json`, `--quiet`.

## Signing in

```sh
lt auth login             # opens your browser
lt auth login --device    # prints a code to enter on another device
lt auth login --force     # re-authenticate instead of reusing the SSO session
```

`lt` never sees your password. The browser and Keycloak handle it, which is
what keeps MFA, session revocation and account state working — this flow does
not route around any of them.

The default flow runs OAuth 2.1 authorization code + PKCE and catches the
redirect on a loopback port. Use `--device` wherever that cannot work: a
container, an SSH session, a CI runner, a machine with no browser. Nothing
listens on a port in that mode.

Credentials are cached at `credentials.json` under your OS's config
directory — `~/.config/libretimes/` on Linux (or `$XDG_CONFIG_HOME`, if set),
`~/Library/Application Support/libretimes/` on macOS, `%AppData%\libretimes\`
on Windows — at mode `0600`, keyed by issuer and client, so a dev login and a
production login coexist rather than evicting each other. Only the refresh
token is durable — access tokens last five minutes and are bought fresh as
needed.

> On Windows the file has no Unix permission bits; `%AppData%` is user-scoped
> by default, which is where the same protection comes from instead.

`LIBRETIMES_TOKEN` overrides the cache entirely, for a one-off against another
account.

`lt auth status` names the account each cached credential belongs to. Worth a
glance before an import: the cache holds one credential per deployment, so
signing in as a second account replaces the first, and `publish` writes as
whoever holds the token.

### After a server-side sign-out

If a command fails with `session_revoked`, the Keycloak *session* was ended
rather than the token expiring. Revocation is keyed on the session id and a
refresh keeps that id, so refreshing cannot help — and a plain `lt auth login`
may reuse the same session through the browser's SSO cookie and fail again.
Force a real re-authentication:

```sh
lt auth login --force
```

### If your connection is slow

The timeouts are sized for a laptop rather than a datacenter, but a bad link is
a bad link. `LT_TIMEOUT_SECONDS` scales all of them together:

```sh
LT_TIMEOUT_SECONDS=300 lt publish ./content/*.md --yes
```

## Publishing

Publishing creates a hosted text on LibreTimes, and the platform casts it into
your Works automatically -- it appears at `/works/<id>`, on your profile's Works
tab and in the works index, with no second step and nothing to claim.

The frontmatter *is* the import manifest. There is no second file mapping
documents to fields.

```yaml
---
title: Интегралы, содержащие логарифм
description: Справочная таблица неопределённых интегралов с логарифмом.
import_key: libretimes-content/reference/ru/integrals-logarithmic
type: reference          # required — there is no default
language: ru
license: cc_by_sa
visibility: public       # public | private | by_link
ai_usage: assisted       # none | assisted | mostly_ai | fully_ai
is_draft: true
tags: [интегралы, логарифм, справочник]
---
```

Four things worth knowing before a first publish:

- **`import_key` is what makes a re-run an update instead of a duplicate.**
  Without one, `lt` derives a key from the file's directory and name
  (`lt:ru/integrals`). That is enough to keep a `ru/` and an `en/` tree of the
  same articles distinct, but setting it explicitly is better — a key derived
  from a path breaks when you move the file.
- **`type` is required and has no default.** A file without one fails before
  any request is made.
- **`tags` and `category_ids` take either form.** A YAML list is the natural
  spelling; a comma-separated string (`tags: a, b, c`) is also accepted, because
  that is what the corpus already uses.
- **`category_ids` are UUIDs, not slugs.** `mathematical-analysis` is a
  category *key*; the API wants the id it maps to, and a key is rejected. Look
  one up from the public category list — no token needed:

  ```sh
  curl -s 'https://api.libretimes.io/libretimes/v1/categories?limit=100' \
    | jq -r '.items[] | "\(.id)  \(.key)"'
  ```

  It is paginated (`has_more`, `next_cursor`), so page through it if you are
  building a full mapping. Up to 25 per work, 10 tags.

`--dry-run` reports `create` / `update` / `blocked` per file and writes
nothing, so the first real run is never a guess. Writes need `--yes`.

`lt publish content/*.md` works the same way on every platform: `lt` expands
`*`, `?` and `[...]` itself rather than relying on the shell, since neither
PowerShell nor `cmd.exe` does that before handing the process its arguments. A
literal path always wins — a real file named `[draft].md` still resolves —
and `**` is refused outright rather than silently matching only one directory
level, which is all it would otherwise mean.

### Why a deleted work is `blocked` rather than updated

Deleting a work is a *soft* delete: the row keeps its `import_key` and
its slot in the unique index. So a key belonging to something you deleted
cannot be reused, and `lt` says so and stops rather than resurrecting it.
Restoring work its author withdrew is a person's decision, not an import's.

### Crediting other people

`contributors` puts names on the byline besides yours. You own the publication
and are on it already.

```yaml
---
title: Lecture 1. Limits
type: lecture
import_key: courses/ivanov/analysis/01
contributors:
  - profile: "@ivanov-ii-a1b2c3"   # a handle (quoted: YAML cannot start a bare value with @)
    role: lecturer
  - profile: 019f9073-76e7-7d32-b4f7-3a0b02a132bf   # or a profile_id
    role: translator
---
```

- **`role`** is one of `author`, `advisor`, `editor`, `translator`, `curator`,
  `lecturer`, and defaults to `author`. Every role but `author` shows as a
  credit after the byline ("Lectures by ..."). Course notes credit the person
  who gave the lectures as `lecturer`; whoever wrote the notes is the author.
- **A credit is attribution, never rights.** There is no `access` key: every
  name from a file is `credited`. Editing rights are given in the editor.
- **Whether a credit is immediate is the platform's call, not `lt`'s.** A
  connection (you follow each other) and a profile nobody signs into, such as
  a lecturer's profile made before they join, are credited at once; a
  connection is notified and can remove it. Anyone else is invited, and their
  name appears once they accept.
- **Listing yourself sets your own role.** On a first publish, your entry
  becomes your role on the byline (a translator publishing someone's text, for
  example). On a re-run your role is left as it is.
- **Only additive.** A re-run adds the names that are missing, in file order.
  It never removes a name you took out of the file, and never changes the role
  of a name already there. A declined invitation stays declined: `lt` does not
  ask again. A differing role is reported as `already credited as ...`, and a
  person changes it in the editor.
- **Every name must resolve before anything is written.** A handle that does
  not resolve fails that file, and no publication is made without the credit
  it was meant to carry. Handles are sent as written; the platform does the
  case folding and follows renamed handles. A `profile_id` is not looked up
  first, so an unknown one fails at the write.

`--dry-run` shows the byline plan under each file: `add` for a name the run
will add, `present` for one already there.

### Courses

A course is the collection that holds one lecture course's notes, in order. It
is described by a manifest, usually the `README.md` in the course's folder:

```yaml
---
import_key: ivanov/analysis-1          # required; identifies the course on re-runs
title: Analysis 1 (autumn 2026)         # required
language: en                            # required; the language of title and description
description: Notes from the lectures.   # optional
visibility: public                      # public, authenticated, connections or owner
category_ids: [019f8f79-4915-7fa3-aa13-4604b0de40b5]   # optional
lectures:                               # lecture files, relative to the manifest, in order
  - lecture-01.md
  - lecture-02.md
---
Anything below the frontmatter is yours; it is never sent.
```

```sh
lt publish ivanov/analysis-1/lecture-*.md --yes
lt course publish ivanov/analysis-1/README.md --dry-run
lt course publish ivanov/analysis-1/README.md --yes
```

- **Lectures are found by their own `import_key`.** `lt` reads each listed
  file's frontmatter and finds the publication `lt publish` made from it. Change
  a lecture's key in its file and the course follows.
- **Publish lectures first; the course picks them up.** A listed lecture that is
  not on the site yet is skipped (`not_published`), and so is one that is a
  draft or was deleted. Run `lt course publish` again after the next lecture
  and it is added.
- **The manifest decides the order.** Missing lectures are appended and the
  course is put back in the manifest's order if it has drifted.
- **Only additive.** Nothing is removed from a course. Items the manifest does
  not name stay where they are and are counted as `unlisted`.
- **A course is a collection, not a publication.** Its visibility is one of
  `public`, `authenticated`, `connections`, `owner`; there is no `private` or
  `by_link`. The course has no byline: credit the lecturer on each lecture with
  `contributors`.
- **A missing lecture file fails the manifest before anything is written.**

`--dry-run` reports `create` or `update` for the course and `add`, `present` or
`skip` for each lecture.

### Books

A book is one text read one chapter per page -- a textbook, a monograph, lecture
notes rewritten as a continuous text. On the site it is a publication of type
`book` whose text is a tree of chapters rather than one body; `lt book publish`
writes that tree from a directory of markdown files. (A course is different: it
groups lectures that are each a publication of their own. A book's chapters are
not separately published and share one byline and one citation.)

```
my-book/
├── book.md          # the manifest
├── preface.md
├── 01-limits.md     # body only, or frontmatter + body
└── 02-series.md
```

```yaml
---
import_key: ivanov/analysis-book     # required; identifies the book on re-runs
title: Analysis                      # required
language: en                         # required; which of the book's texts to write
description: A first course.         # optional, as are license, visibility, ai_usage,
license: cc_by_sa                    #   is_draft, tags, category_ids, contributors --
visibility: public                   #   the same keys `lt publish` takes
contents:
  - file: preface.md
    kind: front_matter
    label: Preface
  - label: Part I                    # no file: a heading in the contents, not a page
    title: Limits
    children:
      - file: 01-limits.md
        label: "1"
        title: Sequences
        ref: I.1                     # optional; see below
      - file: 02-series.md
        label: "2"
        ref: I.2
---
Anything below the frontmatter is yours; it is never sent.
```

A chapter file is its body, optionally under frontmatter carrying the same
per-chapter keys (`kind`, `label`, `title`, `ref`, `first_line`). Write each key
in the manifest or in the file, not both differently.

```sh
lt book publish my-book/book.md --dry-run
lt book publish my-book/book.md --yes
```

- **`kind`** is one of `volume`, `book`, `part`, `chapter`, `section`,
  `article`, `poem`, `story`, `letter`, `act`, `scene`, `front_matter`,
  `back_matter`. It defaults to `chapter`, or to `part` for an entry with
  children and no file.
- **A chapter is matched to the site by `ref` when it has one, else by its
  position** under its parent. Give chapters a `ref` (`I.1`, `Prop. 12`: the
  book's own numbering) and a reorder moves them, text and all. Without one, a
  reorder moves *text* between chapter slots instead. A manifest where only some
  chapters have a ref gets a warning.
- **The manifest is authoritative for order.** Chapters are created, updated
  and moved to match it; one whose file did not change is `unchanged` and not
  written.
- **A chapter on the site that the manifest does not list stops the run**, with
  the list, before anything is written. Add it back, or pass `--prune` to delete
  it. On a published book a deleted chapter's address (`/read/<n>`) is retired,
  never given to another chapter.
- **The book is published last**, once its chapters have text: a new book is
  created as a draft and taken out of draft at the end of the run, unless the
  manifest says `is_draft: true`. On a re-run the draft state changes only if
  the manifest sets `is_draft`.
- **Reading numbers are the site's.** Until a book is first published they
  follow reading order and may change on every run; from then on a chapter
  keeps its number and a new one takes the next. The result reports each
  chapter's `/read/<n>`.
- **Headings start at `##`.** The site shifts a body's headings so none is above
  H2, and removes unsafe markup; a file it rewrote is reported as an update on
  every run until the file matches, with a warning saying so.
- **A chapter is one page, at most 100 KB.** Images are embedded by URL.
- A key held by a publication that is not a book fails with `not_a_book`: a
  publication cannot change into a book.
- **`language` picks the text to write.** On a new book it is the language the
  book is written in. On an existing one it must be a language the book already
  has text in: the original, or a translation added on the site (a translation
  starts as a copy of the original's contents, and `lt` then fills it). A
  language the book has no text in fails with `no_expression` and writes
  nothing. `lt` never writes one language's manifest into another language's
  text, and it does not create translations.

`--dry-run` reports `create` or `update` for the book, and for each chapter
`create`, `update` (with the fields that change, `position` included) or
`unchanged`, plus any `unlisted` chapters. It walks the same placements as the
real run, so the moves it reports are the moves `--yes` makes.

### Problem sheets

A problem sheet is a publication of `type: problems`, published with
`lt publish` like any other. `lt problems` was removed on 2026-09-16, when
problem sheets became ordinary publications on libretimes.io.

## Pointing lt at a dev stack

This is for people working on the LibreTimes platform itself, whose source is
not public. Everyone else can skip it: with no flags, `lt` talks to
libretimes.io.

```sh
lt --env dev auth login
lt --env dev publish ./content/*.md --dry-run
```

`--env dev` resolves the API base, Keycloak URL, realm and client id together,
so they cannot disagree. The individual environment variables
(`LIBRETIMES_API_BASE_URL`, `LIBRETIMES_AUTH_URL`, `LIBRETIMES_REALM`,
`LIBRETIMES_CLIENT_ID`) are still honoured as overrides.

In prod `lt` talks to `api.libretimes.io/libretimes/v1` and signs in at
`auth.libretimes.io`. `--env dev` is the same shape one level down: the dev
stack behind its proxy (`make compose-up-proxy` in the LibreTimes repo), at
`https://api.libretimes.localhost/libretimes/v1` and
`https://auth.libretimes.localhost`. That needs the one-time mkcert setup the
proxy itself needs; if the dev CA is not trusted, `lt` says so rather than
reporting the host unreachable. `.localhost` names resolve to this machine
without DNS.

Two other ways of running the dev stack serve other hosts, and take the
overrides:

```sh
# `make app` (the proxy over http:// by default)
lt --auth-url http://auth.libretimes.localhost \
   --api-url http://api.libretimes.localhost/libretimes/v1 auth login

# plain `compose up`, no proxy
lt --auth-url http://localhost:8080 \
   --api-url http://localhost:8031/libretimes/v1 auth login
```

Use the pair that matches the stack. Keycloak's hostname, and the issuer every
BFF accepts, follow how the stack was started, so a sign-in begun at another
host fails (`authentication_expired`), and a token from another issuer is
refused.

## Scripting and agents

Every command takes `--json`. Exit codes are fixed:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | the API refused the request |
| 2 | bad arguments |
| 3 | a write was attempted without `--yes` |
| 4 | not signed in |

Progress goes to stderr and results to stdout, so `lt ... --json \| jq` works
without filtering noise out first. See [AGENTS.md](AGENTS.md).

## Development

There is no host Go requirement — everything runs in Docker.

```sh
make check     # gofmt, vet, tests: what CI runs
make test
make build
make cross     # every release target, from this one machine
```

`make cross` is worth running once: `CGO_ENABLED=0` builds all six OS/arch
targets from a single Linux machine, Linux statically linked, and
`darwin/arm64` carrying the ad-hoc signature Apple Silicon requires. That
property is what makes the release workflow a single job.

### Building on Windows

`make` itself is the gap, not Docker — it ships with no Windows edition, is
absent from Git Bash, and the `Makefile` is POSIX shell throughout (`rm -rf`,
`$${t%/*}` expansions). Docker Desktop is a normal Windows install, so run the
one `docker run` a plain `make build` wraps directly:

```powershell
docker run --rm -v "${PWD}:/src" -w /src -e CGO_ENABLED=0 golang:1.25-alpine `
  go build -o lt.exe ./cmd/lt
```

Swap `go build` for `go test ./...` or `go vet ./...` for the other targets.

## License

Apache-2.0. See [LICENSE](LICENSE).

Author content on LibreTimes is CC-BY-SA-4.0 — "Libre" refers to the content.
The platform itself is closed source; this client is the exception, because a
client that only ever speaks the public API has nothing to keep private.

# Windows support

**Status, 2026-09-03: #1, #3, #4, #6, #7 and #8 fixed; #9 decided (documented,
not implemented — see its section). #2 and #5 remain open**, both needing an
operator decision this document cannot make on its own — #2 a paid
Authenticode-signing subscription, #5 a package-manager publishing account.
Each fixed section below says so inline rather than being deleted, so the
original bug and its reasoning stay on the record.

`lt` cross-compiles to `windows/amd64` and `windows/arm64` and the binary runs.
Everything below is what a Windows user hits *after* that, found by running the
`windows/amd64` build on a real machine rather than by reading the code.

Test bed: Windows 11 Home 26200, Windows PowerShell 5.1, `cmd.exe`, and Git
Bash; Docker Desktop for the toolchain. `lt` was driven against a local mock
that impersonates both Keycloak and the BFF, so the whole login and publish
path was exercised end to end without touching production.

The order is by what blocks a user, not by how hard it is to fix.

---

## 1. No glob expansion, so every documented `publish` invocation fails

**Fixed 2026-09-03.** `resolveFiles` (internal/cli/publish.go, shared by every
publish-style command) now expands `*`/`?`/`[...]` itself when an argument
does not exist as a literal path — unconditionally, not gated on `GOOS`, so a
shell that already expanded a glob hands it literal paths with nothing left to
match. `**` is refused outright with a message naming the alternative, rather
than silently matching one level the way `filepath.Glob` alone would (the
"worst of the three" outcome named below) — no doublestar dependency was
added. The README's own `**` example is corrected to `*`.

**Severity: blocking.** This is the one that makes `lt publish` unusable on
Windows.

`publish` stats each argument directly ([internal/cli/publish.go:79](internal/cli/publish.go#L79))
and relies on the shell having expanded the wildcard first. Bash does. Neither
PowerShell nor `cmd.exe` does — Windows shells hand the literal string to the
process and expect the program to expand it, which is why `findstr`, `del` and
every native Windows tool do their own globbing.

```
PS> lt publish content\ru\*.md --dry-run
no such file: content\ru\*.md                                  # exit 2

C:\> lt.exe publish content\ru\*.md --dry-run
no such file: content\ru\*.md                                  # exit 2

$ ./lt.exe publish content/ru/*.md --dry-run                   # Git Bash: works,
  create   lt:ru/integrals    content/ru/integrals.md          # bash expanded it
```

Every example in [README.md](README.md) and [AGENTS.md](AGENTS.md) is a glob —
`lt publish content/**/*.md --dry-run`, `lt publish ./content/*.md --yes` — so a
Windows user following the documentation gets exit 2 on their first command and
nothing that explains why.

The error message makes it worse. It ends *"Pass files, or expand a glob in your
shell"* ([internal/cli/publish.go:85](internal/cli/publish.go#L85)) — advice that
is impossible to follow in the shell they are standing in.

**What to implement.** Expand internally: for each argument, if it contains a
wildcard metacharacter (`*`, `?`, `[`) **and** does not exist as a literal path,
glob it and splice in the matches; if the glob matches nothing, fail naming the
pattern. Gate it on `runtime.GOOS == "windows"` or run it everywhere — running
it everywhere is what `gh` and `rg` do, and it is a no-op under a shell that
already expanded, since the expanded arguments contain no metacharacters. A
literal-path check first is what keeps a real file named `[draft].md` working.

Two details worth settling in the same change:

- **`**` is in the README and `filepath.Glob` does not implement it.** It treats
  `**` as a single `*`, so `content/**/*.md` silently matches only one level
  down. Either pull in a doublestar matcher or correct the README — quietly
  matching a subset of what was asked for is the worst of the three.
- The directory-argument message needs rewording at the same time, since it
  currently names a fix that does not exist on this platform.

---

## 2. The Windows binary is unsigned, and Smart App Control refuses to run it

**Open — needs an operator decision (an Azure Trusted Signing subscription or
equivalent) this document cannot make.** The README now names the problem next
to the macOS quarantine note and points here, so at least the failure is
documented rather than silent; the signing itself is not done.

**Severity: blocking, on any machine with Smart App Control on.**

A freshly built `lt.exe` does not start:

```
PS> .\lt.exe version
Program 'lt.exe' failed to run: An Application Control policy has blocked this file

PS> (Get-AuthenticodeSignature .\lt.exe).Status
NotSigned

PS> (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy').VerifiedAndReputablePolicyState
1          # Smart App Control: enforced
```

Reproduced from three directories (`F:\`, `%TEMP%`, `%USERPROFILE%\Downloads`),
so it is the binary, not the location. This is a hard block, not a prompt — no
"Run anyway", no way past it short of turning Smart App Control off machine-wide
(which is one-way: re-enabling it requires a Windows reinstall).

Smart App Control is on by default on clean Windows 11 installs and enforces
that an executable is either Authenticode-signed or carries positive reputation
from Microsoft's Intelligent Security Graph. A newly released `lt.exe` has
neither. Note that this affects *new* builds specifically: an older `lt.exe` on
the same machine still ran, which is exactly the trap — it works for whoever
built it and fails for everyone who downloads the release.

Separately, and more mildly: the release `.zip` downloaded through a browser
carries the Mark-of-the-Web, so SmartScreen adds an "unrecognized app" warning
even where Smart App Control is off.

This is the exact counterpart of the macOS Gatekeeper problem the project has
already thought hard about — [README.md](README.md) has a section on
`com.apple.quarantine`, `.goreleaser.yaml` explains the ad-hoc signature, and
the cask strips quarantine explicitly. The Windows half of that story is not
written.

**What to implement.**

- Authenticode-sign the Windows binaries in the release workflow. **Azure
  Trusted Signing** is the current low-friction route: no HSM to hold, ~$10/mo,
  a GitHub Action, and — unlike a fresh OV certificate — the signature inherits
  reputation immediately rather than after a few thousand installs. An EV
  certificate on a token is the alternative and is worse to automate.
- Until it is signed, say so in the README next to the macOS note, and give the
  actual remedy (Smart App Control cannot be bypassed per-file; the practical
  answer is `winget`/`scoop`, or building from source).
- Signing also removes the SmartScreen warning, so it settles both.

---

## 3. Credentials do not land where the README says they do

**Fixed 2026-09-03.** `NewStore` (internal/auth/store.go) now resolves via
`os.UserConfigDir()` instead of the hand-written `os.UserHomeDir()` + `.config`
— `%AppData%` on Windows, `~/Library/Application Support` on Darwin (a second,
previously-unnoticed divergence this fix also corrects), `$XDG_CONFIG_HOME` or
`~/.config` on Linux, unchanged. The old path is read as a one-release
fallback when the new one is empty, so an existing login on any platform is
not silently lost. The two skip messages named below were about a directory
`lt` now genuinely writes to, so they were left as they were rather than
edited.

**Severity: medium — a documentation lie, not a security hole.**

[README.md](README.md) says:

> On Windows the file has no Unix permission bits. It lives under `%APPDATA%`,
> which is user-scoped by default.

It does not. `NewStore` falls back to `os.UserHomeDir()` + `.config`
([internal/auth/store.go:60](internal/auth/store.go#L60)), and on Windows
`os.UserHomeDir()` returns `%USERPROFILE%`. Verified by planting a credential
file at each candidate path and seeing which one `lt auth status` picked up:

```
C:\Users\<user>\.config\libretimes\credentials.json      <- actual
C:\Users\<user>\AppData\Roaming\libretimes\...           <- what the README says
```

Two test skip messages repeat the same wrong claim
([internal/auth/store_test.go:27](internal/auth/store_test.go#L27),
[:51](internal/auth/store_test.go#L51)), which is how it survived: the
justification for skipping the permission assertions on Windows rests on a fact
about a directory `lt` never writes to.

The *security* claim happens to hold anyway — the effective ACL on the real path
is inherited from the profile root and grants only the user, SYSTEM and
Administrators:

```
NT AUTHORITY\SYSTEM        FullControl  inherited=True
BUILTIN\Administrators     FullControl  inherited=True
DESKTOP-...\<user>         FullControl  inherited=True
```

So nothing is exposed. But a Windows user told to look in `%APPDATA%` will not
find the file, and `lt auth logout` is the only way they will ever be told
otherwise.

**What to implement.** Prefer switching the code to `os.UserConfigDir()`, which
returns `%AppData%` on Windows and honours `XDG_CONFIG_HOME` on Unix — that
makes the README true and puts the file where Windows users expect it. Read the
old `%USERPROFILE%\.config` path as a fallback for one release so nobody is
silently signed out, the way v1 credentials are already read in
[`load`](internal/auth/store.go#L131). Fix the two skip messages either way.

---

## 4. Nothing in CI runs on Windows

**Fixed 2026-09-03.** The `test` job in `.github/workflows/ci.yml` is now a
`[ubuntu-24.04, windows-latest]` matrix; `go vet` and `go test` run on both,
`gofmt` and the `go.mod`-tidy check stay ubuntu-only (no OS dependence, so a
second run buys nothing). Windows runs without `-race`, which needs a C
toolchain `windows-latest` does not have wired up for cgo by default.

**Severity: medium.** This is the root cause of everything above.

[.github/workflows/ci.yml](.github/workflows/ci.yml) runs `test`, `cross` and
`goreleaser-check` — all on `ubuntu-24.04`. The `cross` job proves
`windows/amd64` and `windows/arm64` *compile*, and nothing ever executes them.
The two `runtime.GOOS == "windows"` skips in the test suite have therefore never
been evaluated on Windows.

**What to implement.** Add `windows-latest` to the `test` job matrix
(`go test ./...`, without `-race` unless a C toolchain is set up). It is one
matrix line, and it is what would have caught #1 the moment a `publish` test
used a glob.

---

## 5. There is no Windows install path

**Open — needs an operator decision (a winget or Scoop publishing account)
this document cannot make.**

**Severity: medium.**

[install.sh:47](install.sh#L47) tells Windows users:

```
on Windows use the .zip from https://github.com/.../releases, or Scoop
```

There is no Scoop manifest, no Scoop bucket, and no winget manifest anywhere in
this repo or in `.goreleaser.yaml` — a grep for `scoop|winget|choco` across the
tree returns that one line, which advertises a method that does not exist.

That leaves the manual `.zip`, which is where the rest of the story falls down.
[README.md](README.md) makes a deliberate point about verification:

> The installer verifies the archive against the release `checksums.txt` and
> **aborts if it cannot** [...] An install that silently skipped the check would
> be indistinguishable from one that passed it.

Windows users get no such path. There is no `install.ps1`, so the documented
Windows install is "download a zip, unpack it, put it on your PATH" with the
checksum step left as an exercise — the exact failure mode the installer's
design exists to rule out. And per #2, the binary they unpack may not start.

**What to implement**, roughly in order of payoff:

1. A **winget** manifest, or a **Scoop** bucket, or both. GoReleaser generates
   either (`winget:` / `scoops:`) from the config already present, both verify a
   sha256 by construction, and either one makes the `install.sh` message true.
   winget ships in Windows 11 and is the closer analogue to Homebrew.
2. An **`install.ps1`** mirroring `install.sh`, including the mandatory checksum
   (`Get-FileHash -Algorithm SHA256`) and the same refuse-rather-than-warn rule,
   so `irm ... | iex` is a documented path.
3. `gh attestation verify` works on Windows and is already produced by the
   release workflow — worth naming in the Windows section of the README.

---

## 6. `make build` is not a Windows instruction

**Fixed 2026-09-03.** Documented, not scripted: the README's Development
section now carries the raw `docker run ... go build` equivalent for
PowerShell, rather than a new `build.ps1` to maintain and keep in sync with
the `Makefile`.

**Severity: low.**

> **From source** — needs Docker, not Go:
> ```sh
> make build     # produces ./lt
> ```

Docker Desktop is a normal Windows install. `make` is not — it ships with no
Windows edition, is absent from Git Bash, and is not on `PATH` on this machine
in any shell. The "no host Go requirement" property is real and worth keeping;
it just is not reachable from the documented command here.

The `Makefile` is POSIX-shell throughout as well — `rm -rf` in `clean`, `$${t%/*}`
expansions in `cross`, `/tmp` paths in `snapshot`.

**What to implement.** Either document the raw equivalent in the README's
Windows section (it is one line, and it is what was run to verify this
document):

```powershell
docker run --rm -v "${PWD}:/src" -w /src -e CGO_ENABLED=0 golang:1.25-alpine `
  go build -o lt.exe ./cmd/lt
```

or ship a small `build.ps1` wrapping the same `docker run` calls as `build`,
`test` and `check`.

---

## 7. The credential file is written non-atomically

**Fixed 2026-09-03.** `Store.write` now writes to `credentials.json.tmp` in
the same directory at `0600` and `os.Rename`s it over the target, exactly as
suggested below.

**Severity: low, cross-platform, more likely to bite on Windows.**

[`write`](internal/auth/store.go#L163) opens the real path with `O_TRUNC` and
writes in place. A crash, a Ctrl-C, or a full disk between the truncate and the
write leaves a truncated file — and [`load`](internal/auth/store.go#L131)
deliberately treats an unparseable file as empty, so the failure surfaces as a
silent "not signed in" with the refresh token gone. The comment above the write
reasons carefully about the *mode* at creation and not at all about the window.

Windows raises the odds rather than creating the problem: a realtime antivirus
scanner or the search indexer holding the file open can fail the open or the
rename outright, and `%USERPROFILE%` is indexed by default.

**What to implement.** Write to `credentials.json.tmp` in the same directory at
`0600`, then `os.Rename` over the target — atomic on POSIX, and `MoveFileEx`
with `REPLACE_EXISTING` on Windows. The existing `Chmod` still applies to the
temp file before the rename, so the "never world-readable, not even briefly"
property is preserved rather than traded away.

---

## 8. A BOM in `credentials.json` silently signs the user out

**Fixed 2026-09-03.** `Store.load` strips a leading `\xef\xbb\xbf` before
either unmarshal attempt, matching `frontmatter.Split`.

**Severity: low, but a one-line fix.**

Any Windows tool that rewrites the file adds a UTF-8 BOM — `Set-Content
-Encoding utf8` under PowerShell 5.1 does, and so does Notepad. `json.Unmarshal`
rejects the leading BOM, both the v2 and v1 branches in
[`load`](internal/auth/store.go#L131) fall through, and the file is treated as
corrupt: `lt auth status` reports "Not signed in anywhere" over a perfectly good
refresh token. Observed exactly that while setting up the tests for this
document.

The project already decided this question the other way for markdown —
[frontmatter.Split](internal/frontmatter/frontmatter.go#L88) strips a BOM with
the comment *"editors on Windows add one"*. The credential store should be
consistent with it.

**What to implement.** `bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))` in `load`,
before either unmarshal attempt.

---

## 9. Non-ASCII output on a legacy console codepage

**Decided 2026-09-03: document and defer, not implemented.** Recorded in the
README's Windows note rather than fixed — this would be the first non-stdlib
runtime dependency (`golang.org/x/sys/windows`) for a cosmetic problem modern
default terminals (Windows Terminal, PowerShell 7) do not have. A decision,
not an oversight; revisit if `conhost`/PowerShell 5.1 usage turns out to
matter more than expected.

**Severity: cosmetic; note it and decide.**

`lt` writes UTF-8. A console left on the legacy OEM codepage (437 here, 866 on
Russian installs) renders a Cyrillic title or import key as mojibake. Windows
Terminal and PowerShell 7 default to UTF-8 and are fine; `conhost` and
PowerShell 5.1 are not.

The corpus in the README's own example is Russian
(`Интегралы, содержащие логарифм`), so this is not hypothetical for the users
this tool was built for.

**What to implement**, if it is judged worth the dependency: call
`SetConsoleOutputCP(CP_UTF8)` on startup behind a `_windows.go` build tag, via
`golang.org/x/sys/windows`. That is the first non-stdlib runtime dependency the
CLI would take, so "document it and leave it" is a defensible answer — but it
should be a decision rather than an oversight.

---

## What was verified working

Recorded so nobody re-tests it. All against the `windows/amd64` build on the
machine described above.

| Area | Result |
|---|---|
| `lt auth login` — browser + PKCE, full flow | works end to end |
| `rundll32 url.dll,FileProtocolHandler` URL delivery | byte-identical; percent-encoding intact, `&` intact |
| Loopback listener on `127.0.0.1:0` | binds; no Windows Firewall prompt |
| Credential cache: save, scope by issuer+client, refresh, `logout` | works |
| `XDG_CONFIG_HOME` | honoured on Windows too |
| `auth status` / `auth token` / `whoami`, human and `--json` | works |
| CRLF markdown | normalised to `\n` in the published body |
| UTF-8 BOM in a markdown file | tolerated |
| Cyrillic file names and content | works |
| Backslash paths → import keys | `content\ru\integrals.md` → `lt:ru/integrals` |
| Exit codes 0 / 1 / 2 / 4; `--json` envelope on stderr | as documented |
| Directory passed to `publish` | rejected (message needs rewording, see #1) |
| `LIBRETIMES_TOKEN` override | works |

One thing noticed in passing that is not Windows-specific: `--dry-run` requires
a token, because the plan is computed from a lookup of your own publications
([internal/cli/publish.go:99](internal/cli/publish.go#L99)). That is the right
call and the comment explains it well — but it does mean a user cannot check
that their file arguments resolve at all until they have signed in, which is a
sharper edge on the platform where argument resolution is the thing that breaks.

---

## Suggested order

1. ~~**Glob expansion** (#1)~~ — done.
2. ~~**A `windows-latest` line in CI** (#4)~~ — done, and it keeps #1 fixed.
3. **Authenticode signing** (#2) — still open. Longest lead time, so start it
   early; the release is not really shippable to Windows without it. Needs an
   operator decision (a paid signing subscription) this document cannot make.
4. **winget or Scoop** (#5) — still open. Completes the install story and
   makes `install.sh`'s message true. Needs an operator decision (a
   package-manager publishing account).
5. ~~#3, #6, #7, #8~~ — done.
6. ~~#9~~ — decided: document and defer.

What is left here is entirely #2 and #5, and both are blocked on the same
kind of thing: an account or subscription only the operator can create.

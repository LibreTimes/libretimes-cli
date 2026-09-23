# Driving `lt` from an agent

`lt` is built to be run by a program as readily as by a person. This file is
the contract; treat everything in it as stable.

## The shape

Progress and errors go to **stderr**. Results go to **stdout**. So this is
always safe:

```sh
lt whoami --json | jq -r .username
```

Every command accepts `--json`. Use it — the human-readable output is for
humans and its layout is not a contract.

## Exit codes

| Code | Meaning | What to do |
|---|---|---|
| 0 | success | continue |
| 1 | the API refused the request | read the error; it says whether retrying helps |
| 2 | bad arguments | fix the invocation; do not retry unchanged |
| 3 | a write was attempted without `--yes` | re-run with `--yes` once you intend the write |
| 4 | not signed in | run `lt auth login --device`; do not retry the original command first |

## Signing in without a browser

The default login opens a browser and listens on a loopback port. In a
container, over SSH, or in CI, neither is available:

```sh
lt auth login --device
```

This prints a short URL and a one-time code. A human authorizes on whatever
device they do have; `lt` polls until that completes and caches the result.
After that, every other command works unattended until the refresh token
expires.

For a fully non-interactive environment, mint a token elsewhere and pass it:

```sh
LIBRETIMES_TOKEN=... lt whoami --json
```

`lt auth token` prints a valid access token on stdout, refreshing it if needed
— useful for handing a credential to something that is not `lt`. Treat that
output as a secret.

## Error codes

`--json` failures print an envelope on **stderr** (stdout stays empty, so a
`| jq` pipeline never finds an error where the command's documented shape was
promised). Per-file `publish` results carry the same codes on stdout.

```
$ lt whoami --json          # stderr, exit 1
{
  "error": {
    "code": "session_revoked",
    "message": "This session was signed out server-side, ..."
  }
}
```

`--json` failures and per-file `publish` results carry a stable `code`:

| Code | Retry? |
|---|---|
| `not_found` | no |
| `unauthenticated` | after `lt auth login` |
| `onboarding_required` | **no** — the account has no LibreTimes profile; only a human completing onboarding fixes it |
| `forbidden` | no |
| `conflict` | usually no; read the message |
| `validation_error` | yes, after correcting the named field |
| `rate_limited` | yes, after a pause |
| `upstream_error` | yes, later |
| `timeout`, `network_error` | yes, once |
| `http_error` | a status the client has no mapping for; read the message. Not usually worth retrying |
| `session_revoked` | **no** — the Keycloak session was ended server-side. A refresh keeps the same session id and fails identically, and so may a plain `lt auth login`. Run `lt auth login --force` |
| `bad_arguments`, `needs_confirmation`, `not_signed_in`, `local_error` | CLI-level failures that never reached the API |
| `api_error` | exit 1 with no more specific code; read the message |

**`not_found` is deliberately ambiguous.** It means "either it does not exist,
or it is not visible to you", and the message is byte-identical for both. Do
not try to infer which — the ambiguity is a privacy guarantee, not a gap, and
the API will not tell you either.

## Publishing safely

Always plan first:

```sh
lt publish ./content/*.md --dry-run --json
```

Each file resolves to one of four actions:

| Action | Meaning |
|---|---|
| `create` | no publication holds this `import_key` yet |
| `update` | one does, and it will be updated in place |
| `blocked` | a **deleted** publication holds this key |
| `failed` | the file or the request was rejected; see `message` |

`blocked` is not a transient failure and retrying will not clear it. Deleting a
publication is a soft delete, so the row keeps its `import_key` and its index
slot. Restoring work its author withdrew is a person's decision — surface it
and stop.

Then commit to it:

```sh
lt publish ./content/*.md --yes --json
```

`--dry-run` reports exactly what `--yes` will do, including `blocked`. It never
promises work the real run would refuse.

## Byline credits

A file's `contributors` (`profile`: `"@handle"` or a profile_id; `role`: a
contributor role, default `author`) come back under each result as
`contributors`, each with an `action`:

| Action | Meaning |
|---|---|
| `add` | the run puts this name on the byline (on a create, all of them) |
| `present` | already on the byline in some status (pending, accepted or declined); nothing is sent |

`current_role` appears when a `present` name holds a different role from the
one the file asks for. `lt` does not change it; tell a person.

Things to branch on:

- A contributor that does not resolve fails the **whole file** before any
  write, with `not_found` and the contributor named in `message`. Nothing is
  created without its credit.
- A re-run whose publication was updated but whose credit then failed reports
  `failed` with `publication_id` set and a message starting `updated, but
  crediting`. Re-running is safe: names already present are skipped.
- Credits are only ever added. Removing a name from the file removes nothing.
- The signed-in account in `contributors` sets its own role on a create and
  is `present` on a re-run. Nothing lets you add a byline entry under another
  identity.

## Courses

`lt course publish MANIFEST... --dry-run --json` plans one course per manifest
(see README.md for the frontmatter). Each result has `action` (`create`,
`update`, `failed`) and `collection_id`, and a `lectures` array, each with an
`action`:

| Action | Meaning |
|---|---|
| `add` | the run adds this lecture to the course |
| `present` | already in the course; nothing is sent |
| `skip` | not added; `reason` says why |

| Reason | Meaning |
|---|---|
| `not_published` | no publication holds the lecture's `import_key` yet. Publish it, then re-run |
| `draft` | a draft holds it |
| `deleted` | a deleted publication holds it. Same rule as `blocked`: a person decides |

A skip does not fail the course and does not change the exit code. Also on the
result: `reordered` (the run restores the manifest's order) and `unlisted`
(items in the course that no listed lecture accounts for; they are left alone).

Branch on these:

- A listed file that cannot be read, or two files with one `import_key`, fails
  the whole manifest before any request.
- `failed` with `collection_id` set and a message starting `created, but` or
  `updated, but` means the collection was written and adding or reordering
  lectures was not. Re-running is safe.
- `failed` with code `unsupported_server` means the API predates collection
  import keys. `lt` removed the collection it had just created, because every
  later run would otherwise create another. Do not retry until the platform is
  updated.
- Nothing is ever removed from a course, except that rollback.

## Books

`lt book publish MANIFEST... --dry-run --json` plans one book per manifest (see
README.md for the manifest and the chapter files). Each result has `action`
(`create`, `update`, `blocked`, `failed`), `publication_id`, `expression_id`,
`published` (the run took the book out of draft), `warnings`, and a `chapters`
array in reading order, each with `depth`, `kind`, `label`, `title`, `ref`,
`file`, `division_id`, `reading_number` and an `action`:

| Action | Meaning |
|---|---|
| `create` | the run adds this chapter |
| `update` | it exists and changes; `changes` lists the fields (`kind`, `label`, `title`, `first_line`, `ref`, `body`, `position`) |
| `unchanged` | it exists and matches; nothing is sent |

Chapters on the site that the manifest does not list come back under
`unlisted`, with action `unlisted`, or `delete` under `--prune`.

Branch on these:

- `failed` with code `unlisted_chapters` means the site has chapters the
  manifest does not name and `--prune` was not given. Nothing was written. Do
  not add `--prune` on your own: deleting a published chapter retires its
  address for good. Surface the list to a person.
- `failed` with code `not_a_book`: the key is held by another type of
  publication, which cannot become a book. Use another key.
- `failed` with code `no_expression`: the book on the site has no text in the
  manifest's `language`, nor in its own language, to update. A translation is
  added on the site first; this is not transient.
- `failed` with code `validation_error` before any request: a missing file, an
  unknown `kind`, a duplicate `ref`, a key written differently in the manifest
  and in the chapter file, or a chapter over 100 KB. `message` names the file.
- `failed` with `publication_id` set after writes began: some chapters may be
  written. Re-running is safe; it picks up from what is on the site.
- A warning that the platform stored a chapter "with changes" means the file
  will report `update` on every run until it matches (most often a `#`
  heading; start at `##`). It does not fail the run.
- `--dry-run` reports the same chapter actions, moves included, that `--yes`
  will carry out.

Publish a problem sheet with `lt publish` and `type: problems`. `lt problems`
was removed on 2026-09-16, when LibreProblems folded into libretimes.io.

## What `lt` will not do

- **It cannot act as anyone but the signed-in account.** No command acts as a
  profile id, and there is no impersonation flag. Identity comes from the
  token. `contributors` names people to credit; it does not act as them.
- **It cannot create or delete an account, or send mail.** Those belong to the
  identity provider.
- **It cannot restore a deleted publication.** See `blocked` above.
- **It reaches no internal service.** Everything goes through the public API
  with the token you presented.

## Pointing at a dev stack

```sh
lt --env dev auth login --device
lt --env dev publish ./content/*.md --dry-run --json
```

`--env dev` is the dev stack behind its proxy: `https://auth.libretimes.localhost`
and `https://api.libretimes.localhost/libretimes/v1`. A stack started another
way (plain ports, or the proxy over `http://`) needs `--auth-url` and
`--api-url`; README.md lists the pairs. A `network_error` whose message says
the certificate is not trusted is a machine without the dev CA: a person
fixes it, and retrying will not.

Credentials are keyed by issuer, so a dev login does not disturb a production
one. A token cached for one deployment is never spent against another.

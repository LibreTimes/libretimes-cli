#!/usr/bin/env python3
"""Snapshot and drift-check the slice of the LibreTimes API that lt depends on.

Two modes, one implementation:

    contract.py snapshot <spec.json> <out.json> <path>...
    contract.py check    <spec.json> <snapshot.json>

Why both live here rather than inline in the Makefile and the workflow: they
have to agree about what a snapshot *is*, and two copies of that definition is
the same shape of bug this check exists to catch.

What is captured, and why it is not just the path objects: a path object holds
`$ref` pointers, not schemas. `GET /profiles/me` refs `ProfileMe`, so renaming
a field inside `ProfileMe` leaves every path object byte-identical. That is
exactly how the client came to model `profile_id`/`first_name`/`last_name`
against a response that returns `id`/`given_name`/`family_name` — the paths
check was green throughout. So the refs are resolved transitively and the
referenced component schemas are snapshotted too.
"""

import json
import sys


def _walk_refs(node, found):
    """Collect every #/components/schemas/X name reachable from node."""
    if isinstance(node, dict):
        ref = node.get("$ref")
        if isinstance(ref, str) and ref.startswith("#/components/schemas/"):
            found.add(ref.rsplit("/", 1)[1])
        for value in node.values():
            _walk_refs(value, found)
    elif isinstance(node, list):
        for value in node:
            _walk_refs(value, found)


def collect(spec, watched):
    """Return (paths, schemas, missing) for the watched paths, refs resolved."""
    live_paths = spec.get("paths", {})
    components = spec.get("components", {}).get("schemas", {})

    paths, missing, pending = {}, [], set()
    for path in watched:
        if path not in live_paths:
            missing.append(path)
            continue
        paths[path] = live_paths[path]
        _walk_refs(live_paths[path], pending)

    # Transitive closure: a schema can reference further schemas.
    resolved = set()
    while pending - resolved:
        name = sorted(pending - resolved)[0]
        resolved.add(name)
        if name in components:
            _walk_refs(components[name], pending)

    schemas = {n: components[n] for n in sorted(resolved) if n in components}
    return paths, schemas, missing


def _fields(schema):
    props = schema.get("properties")
    return set(props) if isinstance(props, dict) else set()


def _describe_schema_change(name, was, now, report):
    """Field-level detail, because 'CHANGED' alone sends nobody anywhere."""
    gone, added = _fields(was) - _fields(now), _fields(now) - _fields(was)
    if gone:
        report.append(f"         field removed: {sorted(gone)}")
    if added:
        report.append(f"         field added:   {sorted(added)}")

    was_req = set(was.get("required") or [])
    now_req = set(now.get("required") or [])
    if was_req != now_req:
        if now_req - was_req:
            report.append(f"         now required:  {sorted(now_req - was_req)}")
        if was_req - now_req:
            report.append(f"         no longer required: {sorted(was_req - now_req)}")

    for field in sorted(_fields(was) & _fields(now)):
        before, after = was["properties"][field], now["properties"][field]
        if before != after:
            report.append(f"         field changed: {field}")

    if was.get("enum") != now.get("enum"):
        report.append(
            f"         enum: {was.get('enum')} -> {now.get('enum')}")


def check(spec, snapshot):
    """Return (drifted, report_lines)."""
    watched = sorted(snapshot["paths"])
    live_paths, live_schemas, missing = collect(spec, watched)

    report, drifted = [], False

    for path in watched:
        if path in missing:
            report.append(f"GONE     {path}")
            report.append("         lt calls this endpoint and it no longer exists.")
            drifted = True
        elif live_paths[path] != snapshot["paths"][path]:
            report.append(f"CHANGED  {path}")
            was, now = set(snapshot["paths"][path]), set(live_paths[path])
            if was - now:
                report.append(f"         methods removed: {sorted(was - now)}")
            if now - was:
                report.append(f"         methods added:   {sorted(now - was)}")
            drifted = True
        else:
            report.append(f"ok       {path}")

    # Schemas. A name that vanished from the live side matters as much as one
    # whose fields moved: either way the structs no longer match the wire.
    snap_schemas = snapshot.get("schemas", {})
    for name in sorted(snap_schemas):
        if name not in live_schemas:
            report.append(f"GONE     schema {name}")
            drifted = True
        elif live_schemas[name] != snap_schemas[name]:
            report.append(f"CHANGED  schema {name}")
            _describe_schema_change(name, snap_schemas[name], live_schemas[name], report)
            drifted = True

    new_schemas = sorted(set(live_schemas) - set(snap_schemas))
    if new_schemas:
        # Not drift on its own — a watched path may now reference something
        # new — but it is a reason to look, so it is reported without failing.
        report.append(f"note     schemas newly reachable: {new_schemas}")

    return drifted, report


def main(argv):
    if len(argv) < 4:
        sys.exit(__doc__)
    mode, spec_path = argv[1], argv[2]

    with open(spec_path, encoding="utf-8") as fh:
        spec = json.load(fh)

    if mode == "snapshot":
        out_path, watched = argv[3], argv[4:]
        if not watched:
            sys.exit("snapshot needs at least one path")
        paths, schemas, missing = collect(spec, watched)
        if missing:
            sys.exit(f"missing from the live spec: {missing}")
        payload = {
            "_note": (
                "Trimmed to the paths lt calls, with their $refs resolved into "
                "`schemas`. Regenerate with `make snapshot`. "
                "See .github/workflows/contract.yml."
            ),
            "paths": paths,
            "schemas": schemas,
        }
        with open(out_path, "w", encoding="utf-8") as fh:
            json.dump(payload, fh, indent=2, sort_keys=True, ensure_ascii=False)
            fh.write("\n")
        print(f"snapshot: {len(paths)} paths, {len(schemas)} schemas -> {out_path}")
        return 0

    if mode == "check":
        with open(argv[3], encoding="utf-8") as fh:
            snapshot = json.load(fh)
        drifted, report = check(spec, snapshot)
        print("\n".join(report))
        return 1 if drifted else 0

    sys.exit(f"unknown mode {mode!r}")


if __name__ == "__main__":
    sys.exit(main(sys.argv))

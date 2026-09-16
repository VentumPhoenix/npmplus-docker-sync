#!/usr/bin/env python3
"""Vendor the NPM and NPMplus request schemas used by the contract test.

Both servers validate every create/update body against a JSON schema with
`additionalProperties: false`, and the two schemas have drifted apart. The
contract test in internal/npm/contract_test.go marshals a payload for each
resource kind and validates it against the real schema, so a payload struct can
never silently go out of sync with the API again.

The upstream schemas are split across ~100 files linked by relative $refs. This
script downloads both repositories, inlines every $ref of the request bodies
and writes one self-contained schema per (flavour, kind, method):

    internal/npm/testdata/schema/<flavour>/<kind>-<method>.json

Usage:
    python3 scripts/vendor-schemas.py [--npmplus-ref develop] [--npm-ref master]
"""

from __future__ import annotations

import argparse
import io
import json
import pathlib
import sys
import tarfile
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "internal" / "npm" / "testdata" / "schema"

SOURCES = {
    "npmplus": ("ZoeyVid/NPMplus", "develop"),
    "npm": ("NginxProxyManager/nginx-proxy-manager", "master"),
}

# (kind, collection path segment, id placeholder used in the path tree)
KINDS = [
    ("proxy", "proxy-hosts", "hostID"),
    ("redirect", "redirection-hosts", "hostID"),
    ("stream", "streams", "streamID"),
    ("dead", "dead-hosts", "hostID"),
]


def download(repo: str, ref: str) -> dict[str, bytes]:
    """Return the backend/schema tree of a repository, keyed by relative path."""
    url = f"https://codeload.github.com/{repo}/tar.gz/refs/heads/{ref}"
    with urllib.request.urlopen(url, timeout=120) as resp:  # noqa: S310
        blob = resp.read()

    files: dict[str, bytes] = {}
    with tarfile.open(fileobj=io.BytesIO(blob), mode="r:gz") as tar:
        for member in tar.getmembers():
            if not member.isfile():
                continue
            parts = member.name.split("/", 1)
            if len(parts) != 2:
                continue
            rel = parts[1]
            if not rel.startswith("backend/schema/") or not rel.endswith(".json"):
                continue
            handle = tar.extractfile(member)
            if handle is not None:
                files[rel[len("backend/schema/"):]] = handle.read()
    if not files:
        raise SystemExit(f"no schema files found in {repo}@{ref}")
    return files


def resolve(node, doc: str, files: dict[str, bytes], stack: tuple[tuple[str, str], ...]):
    """Inline every $ref reachable from node.

    doc is the schema-relative path of the file the node came from, so both
    relative file refs ("../common.json#/properties/id") and document-local
    ones ("#/properties/forward_scheme") resolve exactly as the server's loader
    resolves them. A ref already on the stack is replaced by an empty schema:
    the only cycles live in the *response* objects (owner -> user -> ...), which
    no request body reaches, and an empty schema keeps the flattening total.
    """
    if isinstance(node, list):
        return [resolve(item, doc, files, stack) for item in node]
    if not isinstance(node, dict):
        return node

    ref = node.get("$ref")
    if isinstance(ref, str):
        path, _, pointer = ref.partition("#")
        if path:
            target = normalize((pathlib.PurePosixPath(doc).parent / path).as_posix())
        else:
            target = doc
        if target not in files:
            raise SystemExit(f"unresolved $ref {ref!r} from {doc!r}")

        key = (target, pointer)
        if key in stack:
            return {}

        document = json.loads(files[target])
        for segment in [s for s in pointer.split("/") if s]:
            document = document[segment.replace("~1", "/").replace("~0", "~")]

        merged = resolve(document, target, files, stack + (key,))
        # Keep sibling keywords (descriptions, examples) next to the ref.
        extras = {k: resolve(v, doc, files, stack) for k, v in node.items() if k != "$ref"}
        if isinstance(merged, dict):
            merged = {**merged, **extras}
        return merged

    return {key: resolve(value, doc, files, stack) for key, value in node.items()}


def normalize(path: str) -> str:
    """Collapse ".." segments, which PurePosixPath keeps verbatim."""
    out: list[str] = []
    for part in path.split("/"):
        if part in ("", "."):
            continue
        if part == "..":
            if out:
                out.pop()
            continue
        out.append(part)
    return "/".join(out)


def request_schema(files: dict[str, bytes], path: str):
    document = json.loads(files[path])
    body = document.get("requestBody")
    if not body:
        raise SystemExit(f"{path} has no requestBody")
    schema = body["content"]["application/json"]["schema"]
    return resolve(schema, path, files, ())


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--npmplus-ref", default=SOURCES["npmplus"][1])
    parser.add_argument("--npm-ref", default=SOURCES["npm"][1])
    args = parser.parse_args()

    refs = {"npmplus": args.npmplus_ref, "npm": args.npm_ref}
    provenance = ["# Vendored API schemas", ""]
    provenance.append("Generated by scripts/vendor-schemas.py. Do not edit by hand.")
    provenance.append("")

    for flavour, (repo, _) in SOURCES.items():
        ref = refs[flavour]
        files = download(repo, ref)
        target = OUT / flavour
        target.mkdir(parents=True, exist_ok=True)
        provenance.append(f"- `{flavour}/` from https://github.com/{repo} (`{ref}`)")

        for kind, collection, id_segment in KINDS:
            for method, source in (
                ("post", f"paths/nginx/{collection}/post.json"),
                ("put", f"paths/nginx/{collection}/{id_segment}/put.json"),
            ):
                if source not in files:
                    print(f"  skip {flavour}/{kind}-{method}: {source} not in tree", file=sys.stderr)
                    continue
                schema = request_schema(files, source)
                schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
                out = target / f"{kind}-{method}.json"
                out.write_text(json.dumps(schema, indent="\t", sort_keys=True) + "\n")
                print(f"  wrote {out.relative_to(ROOT)}")

    (OUT / "README.md").write_text("\n".join(provenance) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

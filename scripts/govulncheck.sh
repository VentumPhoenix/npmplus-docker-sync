#!/usr/bin/env bash
# Run govulncheck and fail only on findings that are not in .govulncheck-allow.
#
# govulncheck itself has no ignore mechanism, but a hard "any finding fails"
# gate is unusable here: the github.com/docker/docker +incompatible line
# carries advisories whose fix only exists in github.com/moby/moby/v2, so the
# whole module counts as vulnerable forever. See .govulncheck-allow.
#
# Only symbol-level ("called") findings are gated. Module- and package-level
# findings mean the vulnerable code is not reachable and are reported as FYI.
set -euo pipefail

cd "$(dirname "$0")/.."
allow_file=".govulncheck-allow"
report="$(mktemp)"
trap 'rm -f "$report"' EXIT

echo "Running govulncheck..."
# Exit code 3 means "vulnerabilities found", which is not a script failure.
set +e
go run golang.org/x/vuln/cmd/govulncheck@latest -format json ./... >"$report"
status=$?
set -e
if [ "$status" -ne 0 ] && [ "$status" -ne 3 ]; then
  echo "::error::govulncheck failed to run (exit $status)"
  cat "$report"
  exit "$status"
fi

ALLOW_FILE="$allow_file" python3 - "$report" <<'PY'
import json, os, sys

with open(sys.argv[1]) as fh:
    text = fh.read()

# govulncheck streams concatenated JSON objects rather than one array.
decoder, objects, i = json.JSONDecoder(), [], 0
while i < len(text):
    while i < len(text) and text[i].isspace():
        i += 1
    if i >= len(text):
        break
    obj, i = decoder.raw_decode(text, i)
    objects.append(obj)

called, not_called = {}, set()
for obj in objects:
    finding = obj.get("finding")
    if not finding:
        continue
    frame = finding["trace"][0]
    if frame.get("function"):
        called.setdefault(finding["osv"], set()).add(
            "{}.{}".format(frame.get("package", "?"), frame["function"])
        )
    else:
        not_called.add(finding["osv"])
not_called -= called.keys()

allowed = {}
with open(os.environ["ALLOW_FILE"]) as fh:
    for line in fh:
        line = line.split("#", 1)[0].strip()
        if line:
            allowed[line] = True

blocking = sorted(called.keys() - allowed.keys())
accepted = sorted(called.keys() & allowed.keys())
stale = sorted(allowed.keys() - called.keys() - not_called)

if not_called:
    print("\nNot reachable from this binary (informational):")
    for osv in sorted(not_called):
        print("  - {}  https://pkg.go.dev/vuln/{}".format(osv, osv))

if accepted:
    print("\nAccepted via {} (reachable, reviewed):".format(os.environ["ALLOW_FILE"]))
    for osv in accepted:
        symbols = sorted(called[osv])
        print("  - {}  https://pkg.go.dev/vuln/{}".format(osv, osv))
        print("      called: {}{}".format(
            ", ".join(symbols[:3]), " (+{} more)".format(len(symbols) - 3) if len(symbols) > 3 else ""))

if stale:
    print("\n::warning::Stale entries in {} - no longer reported, please remove: {}".format(
        os.environ["ALLOW_FILE"], ", ".join(stale)))

if blocking:
    print("\n::error::{} reachable vulnerability/vulnerabilities not in {}:".format(
        len(blocking), os.environ["ALLOW_FILE"]))
    for osv in blocking:
        print("  - {}  https://pkg.go.dev/vuln/{}".format(osv, osv))
        for symbol in sorted(called[osv])[:5]:
            print("      called: {}".format(symbol))
    sys.exit(1)

print("\nOK: no unreviewed reachable vulnerabilities.")
PY

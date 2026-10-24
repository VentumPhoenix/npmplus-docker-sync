#!/usr/bin/env bash
# Boots one of the stacks in deploy/ and checks that it really works:
#   1. every container becomes healthy (or, for one-shot jobs, exits 0)
#   2. the tool creates the proxy host for the admin UI
#   3. a freshly started, labelled app gets a host and is served through it
#   4. hardened stacks only: that app cannot open a connection to the proxy
#      manager - the isolation holds
#
#   scripts/deploy-smoke.sh deploy/npmplus/2-standard
#
# Needs Docker with Compose, free ports 80/443 and internet access (images,
# NPMplus registers with the Let's Encrypt staging server, CrowdSec its hub).
# Leaves nothing behind unless KEEP_STACK=1.
set -euo pipefail

dir=${1:?usage: $0 deploy/<flavour>/<level>}
[ -f "$dir/compose.yaml" ] || { echo "no compose.yaml in $dir" >&2; exit 2; }

ui_domain=npm.smoke.test
app_domain=whoami.smoke.test
app=deploy-smoke-whoami

compose() { docker compose --project-directory "$dir" -f "$dir/compose.yaml" "${extra[@]}" "$@"; }

# --- configuration ----------------------------------------------------------
cp "$dir/.env.example" "$dir/.env"
set_env() {
  if grep -q "^#\? \?$1=" "$dir/.env"; then
    sed -i "s|^#\? \?$1=.*|$1=$2|" "$dir/.env"
  else
    echo "$1=$2" >> "$dir/.env"
  fi
}
set_env NPM_UI_DOMAIN "$ui_domain"
set_env NPM_ADMIN_EMAIL admin@example.com
set_env NPM_ADMIN_PASSWORD "smoke-$(openssl rand -hex 12)"
set_env ACME_EMAIL smoke@example.com
set_env DOCKER_GID "$(stat -c %g /var/run/docker.sock)"
grep -q '^CROWDSEC_BOUNCER_KEY=' "$dir/.env" && set_env CROWDSEC_BOUNCER_KEY "$(openssl rand -hex 32)"

extra=()
if grep -q 'zoeyvid/npmplus' "$dir/compose.yaml"; then
  # Production Let's Encrypt rejects example addresses and a throwaway stack
  # has no business creating accounts there: register without an e-mail,
  # against staging (same as test/integration).
  override=$(mktemp --suffix=.yaml)
  cat > "$override" <<'EOF'
services:
  npmplus:
    environment:
      ACME_EMAIL: ""
      ACME_SERVER: https://acme-staging-v02.api.letsencrypt.org/directory
EOF
  extra=(-f "$override")
fi

network=$(compose config --format json | python3 -c '
import json, sys
nets = json.load(sys.stdin)["networks"]
print(next(n["name"] for n in nets.values() if n["name"] in ("npm", "npm-frontend")))')

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "::group::state"; compose ps -a || true; echo "::endgroup::"
    for c in $(compose ps -a --format '{{.Name}}'); do
      echo "::group::logs $c"; docker logs --tail 150 "$c" 2>&1 || true; echo "::endgroup::"
    done
  fi
  if [ "${KEEP_STACK:-0}" != 1 ]; then
    docker rm -f "$app" >/dev/null 2>&1 || true
    compose down -v --remove-orphans >/dev/null 2>&1 || true
    rm -f "$dir/.env"
  fi
  exit "$status"
}
trap cleanup EXIT

# --- 1. every container healthy --------------------------------------------
compose up -d
echo "waiting for $dir"
deadline=$((SECONDS + 600))
while :; do
  pending=()
  for c in $(compose ps -a --format '{{.Name}}'); do
    state=$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' "$c")
    read -r status code health <<<"$state"
    case "$status:$health" in
      running:healthy | running:) ;;
      exited:) [ "$code" = 0 ] || { echo "$c exited with $code"; exit 1; } ;;
      *) pending+=("$c=$status${health:+/$health}") ;;
    esac
  done
  [ ${#pending[@]} -eq 0 ] && break
  [ $SECONDS -lt $deadline ] || { echo "timed out waiting for: ${pending[*]}"; exit 1; }
  sleep 10
done
compose ps

# --- 2. the admin UI got its host --------------------------------------------
wait_for_log() {
  local pattern=$1 deadline=$((SECONDS + 120))
  until docker logs npmplus-docker-sync 2>&1 | grep -q -- "$pattern"; do
    [ $SECONDS -lt $deadline ] || { echo "the tool never logged: $pattern"; exit 1; }
    sleep 3
  done
}
wait_for_log "created resource.*key=$ui_domain"
echo "ok: proxy host for the admin UI"

# --- 3. a labelled app is published ------------------------------------------
docker run -d --name "$app" --network "$network" \
  --label npm.proxy.domains="$app_domain" --label npm.proxy.port=80 \
  traefik/whoami:latest >/dev/null
wait_for_log "created resource.*key=$app_domain"
deadline=$((SECONDS + 60))
until curl -fsS -H "Host: $app_domain" http://127.0.0.1/ | grep -q "^Hostname:"; do
  [ $SECONDS -lt $deadline ] || { echo "$app_domain is not served"; curl -sS -H "Host: $app_domain" http://127.0.0.1/ | head -20; exit 1; }
  sleep 3
done
echo "ok: $app_domain served through the proxy"

# --- 4. hardened: the app cannot reach the proxy manager ---------------------
if grep -q 'enable_icc: "false"' "$dir/compose.yaml"; then
  proxy_ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$network\").IPAddress}}" \
    "$(compose ps --format '{{.Name}}' | grep -E '^(npm|npmplus)$')")
  # Any HTTP answer, even an error page, means the connection went through.
  probe="wget -S -T 5 -O /dev/null http://$proxy_ip:80/ 2>&1 | grep -q 'HTTP/'"
  if docker run --rm --network "$network" alpine:3.24 sh -c "$probe"; then
    echo "an app on $network reached the proxy manager at $proxy_ip:80 - isolation broken"
    exit 1
  fi
  echo "ok: an app cannot open connections to the proxy manager"
fi

echo "PASS $dir"

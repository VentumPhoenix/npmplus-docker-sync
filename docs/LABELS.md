# Label reference

The exhaustive list of fields — with aliases, defaults, environment variables
and the API field each one writes to — is generated from the code in
[FIELDS.md](FIELDS.md). This page explains the grammar and the rules around
it.

## Grammar

```
<prefix>.enable=false                     exclude the container
<prefix>.<kind>.<field>                   one resource, index 0
<prefix>.<kind>.<index>.<field>           indexed (Redth's position)
<prefix>.<index>.<kind>.<field>           indexed (our own position)
<prefix>.<field>                          shorthand, <kind> defaults to proxy
<prefix>.<index>.enable=false             switch a single index off
```

| Part | Values | Notes |
|---|---|---|
| `<prefix>` | `npm` by default | Change it with `LABEL_PREFIX`. The separator may be `.` or `-`: `npm-proxy.domains` works. |
| `<index>` | any non-negative integer | Defaults to `0`. Kind and index may appear in either order. |
| `<kind>` | `proxy`, `redirect` (`redirection`), `stream`, `404` (`dead`) | Defaults to `proxy`. |
| `<field>` | see [FIELDS.md](FIELDS.md) | Inside a field name `.`, `_` and `-` are interchangeable. |

Booleans accept `true/false`, `1/0`, `yes/no`, `on/off`. Domain and id lists
are comma, semicolon or whitespace separated.

Because the separators are interchangeable, all of these set the same field:

```yaml
npm.proxy.ssl.hsts.subdomains: "true"
npm.proxy.ssl.hsts_subdomains: "true"
npm.proxy.ssl-hsts-subdomains: "true"
```

> [!NOTE]
> `404` is reserved as a resource type, so it cannot be used as an index.
> Write `npm.404.domains` (index 0, 404 host) or `npm.5.404.domains`.

## Which containers are managed

Every container that carries at least one label of the namespace is
synchronised. Opt a container out with:

```yaml
npm.enable: "false"
```

Set `NPM_EXPOSED_BY_DEFAULT=false` for the opposite policy: then `npm.enable`
must be truthy for a container to be looked at. The sync container itself is
always ignored.

## `domains` is the domain, `host` is the upstream

> [!IMPORTANT]
> **Breaking change in v1.0.0-beta.3.** `host` used to mean the domain name.
> It is now the *upstream target*, as in
> [Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync), and the
> domain is `domains` (alias `domain`).

```yaml
npm.proxy.domains: "app.example.com"   # what the world asks for
npm.proxy.host: "192.168.1.50"         # where it is served from (optional)
```

A configuration that still uses `host` for the domain is rejected with a
message pointing at `domains`, rather than quietly creating a host that
forwards to itself. For redirection and 404 hosts — which have no upstream —
`host` remains an alias for `domains`.

## Multiple resources per container

Each index is an independent resource, and indexes may mix resource types:

```yaml
labels:
  npm.proxy.domains: "app.example.com"
  npm.proxy.port: "8080"

  npm.proxy.1.domains: "metrics.app.example.com"
  npm.proxy.1.port: "9090"

  npm.2.stream.incoming_port: "5432"

  npm.3.redirect.domains: "old-app.example.com"
  npm.3.redirect.forward_domain: "app.example.com"

  npm.4.404.domains: "parked.example.com"
```

Disable one index without deleting its labels:

```yaml
npm.1.enable: "false"
```

## Defaults and where they come from

For every field, the first source that has a value wins:

1. the label for that index — `npm.proxy.1.websockets`
2. the label without an index (which *is* index 0) — `npm.proxy.websockets`
3. the kind specific environment variable — `NPM_PROXY_WEBSOCKETS`
4. the cross-kind environment variable — `NPM_DEFAULT_WEBSOCKETS`
5. the built-in default

Every alias of a field has its own variable, which is why Redth's
`NPM_PROXY_SSL_FORCE` and `NPM_PROXY_HSTS_SUBDOMAINS` work unchanged. The
defaults that the environment changed are logged at start-up.

```yaml
# on the sync container
NPM_PROXY_SSL_FORCE: "true"        # every host redirects to https ...
NPM_PROXY_WEBSOCKETS: "true"
```

```yaml
# ... except this one
npm.proxy.ssl.force: "false"
```

The built-in defaults aim at "one label is enough":

```yaml
labels:
  npm.proxy.domains: "homepage.home.example.com"
```

produces a host with the container's exposed port as upstream, HTTP/2, HTTP/3,
websockets, the exploit blocklist, the matching certificate and the HTTPS
redirect that follows from it.

## Upstream port

`port` (canonically `forward_port`) is the port **inside** the container. It
defaults to `auto`:

* exactly one exposed TCP port → that one,
* several → the first match in `NPM_PORT_PREFERENCE`
  (default `80,8080,3000,8000,443`),
* none of them listed, or no exposed port at all → the resource is skipped
  with a message naming the ports it found.

## Certificates

`certificate` (aliases `certificate_id`, `ssl.certificate.id`, `ssl` for
streams) accepts:

| Value | Meaning |
|---|---|
| *(unset)* | `NPM_DEFAULT_CERTIFICATE`, which is `auto` out of the box |
| `auto` | pick the best matching certificate |
| `12` | that certificate id |
| `app.example.com`, `*.home.example.com` | a certificate covering this domain |
| `name:My wildcard` | by `nice_name` |
| `new`, `letsencrypt` | request a new Let's Encrypt certificate |
| `none`, `off` | deliberately no certificate |

### How `auto` chooses

Candidates are first filtered: client CAs (`provider: mtls`), deleted and
expired certificates are out. The rest are ranked:

1. **exact** — the certificate covers exactly these domains,
2. **exact + SANs** — every domain matches exactly, plus further names,
3. **mixed** — every domain is covered, some by a wildcard,
4. **wildcard** — every domain is covered by a wildcard.

Ties are broken by the longest remaining validity, then the lowest id. A
wildcard covers exactly one label (RFC 6125): `*.home.example.com` matches
`a.home.example.com`, but neither `home.example.com` nor
`a.b.home.example.com`. Comparison is case-insensitive and internationalised
names are converted to punycode first.

If no single certificate covers every domain, `NPM_CERTIFICATE_PARTIAL`
decides: `primary` (default) takes a certificate for the first domain and warns
about the rest, `none` attaches nothing.

A certificate that is already attached, still valid and still covering the
host is kept. It is only replaced when a candidate matches in a *better* class
— so importing another wildcard does not shuffle existing hosts around, while
a new exact certificate does take over on the next run. Certificates created
in the NPM UI are picked up within `CERTIFICATE_POLL_INTERVAL` (default `1m`).

### Requesting a certificate

```yaml
npm.proxy.certificate: "new"
npm.letsencrypt.email: "admin@example.com"   # upstream NPM only
npm.letsencrypt.agree: "true"                # upstream NPM only
```

NPMplus takes the ACME account from its own `ACME_EMAIL`, so the two
`letsencrypt.*` labels are not needed there — and not demanded either. Against
upstream nginx-proxy-manager they stay mandatory and the write is refused
without them.

DNS-01 (required for wildcards) works on both:

```yaml
npm.proxy.domains: "*.example.com"
npm.proxy.certificate: "new"
npm.letsencrypt.dns_challenge: "true"
npm.letsencrypt.dns_provider: "cloudflare"
npm.letsencrypt.dns_credentials: "dns_cloudflare_api_token=..."
npm.letsencrypt.propagation_seconds: "60"
```

> [!CAUTION]
> DNS credentials in labels are visible to anyone who can run
> `docker inspect`. Prefer creating such certificates once in the NPM UI — the
> automatic selection will find them.

With `NPM_CERTIFICATE_AUTO_CREATE=true` a host that finds no matching
certificate requests one instead of staying on plain HTTP. It is off by
default because of the Let's Encrypt rate limits and because DNS and port 80
have to be right for the challenge to succeed.

### TLS cascade

The server drops TLS settings that cannot apply, and so does this tool before
comparing state — otherwise the two would never agree and every event would
trigger another update:

```
no certificate      -> ssl.forced          = false
no ssl.forced       -> ssl.hsts            = false
no ssl.hsts         -> ssl.hsts_subdomains = false
```

`ssl.forced` defaults to `auto`, which means "on as soon as a certificate is
attached".

## Access lists

Access lists may be given by id or by name; names are resolved against
`/api/nginx/access-lists` on every run:

```yaml
npm.proxy.access_list: "Intern, VPN"
npm.proxy.access_list: "2,5"
npm.proxy.access_list_type: "public"   # drop the lists again
```

An unknown name skips the resource with a warning. It is never treated as
"public": a typo must not put a protected host on the open internet.

Custom locations carry their own access list on NPMplus, where the fields are
mandatory. The default is `global`, which means "inherit the proxy host's".

Passing more than one id to an upstream NPM server is refused before the
request, with a message saying so.

## Custom locations

```yaml
npm.proxy.domains: "app.example.com"
npm.proxy.port: "8080"

npm.proxy.location.0.path: "/api"
npm.proxy.location.0.forward_port: "3000"

npm.proxy.location.1.path: "/ws"
npm.proxy.location.1.advanced_config: "proxy_read_timeout 86400s;"

npm.proxy.location.2.path: "/static"
npm.proxy.location.2.type: "prefer"        # nginx "^~ "
npm.proxy.location.2.fancyindex: "true"
```

`type` maps to the nginx location modifier: `prefix` (default), `exact` (`= `),
`regex` (`~ `), `iregex` (`~* `), `prefer` (`^~ `) and `named` (`@`). Upstream,
access list and every NPMplus switch are inherited from the host unless the
location overrides them. Blocks are ordered by `<n>`; gaps are allowed.

## NPMplus-only fields

`ssl.http3`, `noindex`, `crowdsec_appsec`, `request_buffering`,
`response_buffering`, `upstream_compression`, `fancyindex`, `x_frame_options`,
`auth_request`, `auth_request_upstream`, `location_config` and the stream
extras (`proxy_protocol`, `proxy_tls`, `advanced_config`, `description`) exist
only in NPMplus. Against upstream nginx-proxy-manager they are left out of the
request and reported once per resource — a better default must not break the
other flavour.

Three of them are inverted in the API. The labels are positive and negated on
the way in:

```yaml
npm.proxy.crowdsec_appsec: "false"    # sends npmplus_crowdsec_appsec: true
npm.proxy.disable_crowdsec_appsec: "true"   # the same thing, spelled out
```

## Streams

Streams have no domain names: their identity is the **incoming port**, so NPM
allows exactly one stream per port.

```yaml
# PostgreSQL over TCP
npm.stream.incoming.port: "5432"
npm.stream.forward.port: "5432"
npm.stream.ssl: "db.example.com"       # certificate by domain

# WireGuard over UDP, on a second index
npm.1.stream.incoming_port: "51820"
npm.1.stream.protocol: "udp"
```

Setting both `tcp` and `udp` to `false` is rejected: NPM would have nothing to
forward. `description` defaults to the container name, which is what the
NPMplus UI shows.

> [!IMPORTANT]
> NPM only accepts stream ports that its own container publishes. Publish the
> port on the NPM container first, otherwise the stream exists in the database
> but nothing listens.

## The `enabled` flag

`enabled` is the one field that does **not** travel in the create/update body:
both APIs reject the property in their schemas and expose dedicated endpoints
instead (`POST <collection>/{id}/enable` and `/disable`). Every resource is
therefore created in the enabled state and switched afterwards if the label
says otherwise.

The reconcile loop compares the label with the live state and calls the
endpoint only when they differ, so `enabled=false` takes effect on the run
after the resource is created, and toggling it in the NPM UI is corrected on
the next reconcile.

## Upstream host resolution

The upstream of proxy hosts and streams is resolved in this order:

1. the explicit `host` / `forward_host` label,
2. with `NPM_NETWORK` set: the container's IP **in that network only**. A
   container that is not attached to it is skipped with a warning naming the
   networks it is on — an address from elsewhere, and the container name
   alike, would be unreachable for NPM and yield a silent `502`.
   `NPM_NETWORK_STRICT=false` restores the fall-through of earlier releases.
3. with `NPM_CONTAINER_NAME` set: the networks that container is attached to,
4. otherwise: the container's IP in a network the sync container is attached
   to, else the IP of the first network (alphabetically), else the container
   name.

Only IPv4 addresses are used — NPM writes the value straight into `proxy_pass`,
where a bare IPv6 address would be invalid.

Disable the behaviour globally with `RESOLVE_CONTAINER_IP=false`, or per
resource with `npm.proxy.resolve_ip: "false"`.

## Unknown labels

A label in the namespace that names no field produces a warning with a
suggestion:

```
unknown label npm.proxy.ssl.forcd - did you mean npm.proxy.ssl.forced?
```

The rest of the resource is still created. With `STRICT_LABELS=true` the
resource is skipped instead, so a typo cannot quietly leave a host without the
setting it was supposed to have.

## Validation

A resource is rejected (and the reason logged with the offending label) when:

* a required field is missing (`domains`, `forward_domain`, `incoming_port`,
  `location.<n>.path`),
* the upstream port can neither be read from a label nor guessed,
* a port is not a number in `1–65535`,
* an enum value is not in the list the field allows,
* a domain contains a path, space, scheme or port,
* a boolean or numeric label cannot be parsed,
* a redirect status code is outside `300–308`,
* a stream has neither TCP nor UDP enabled,
* `ssl.forced=true` is combined with `certificate: none`,
* an access list name cannot be resolved,
* `letsencrypt.dns_challenge=true` is set without `letsencrypt.dns_provider`.

Invalid definitions are skipped individually: the other indexes of the same
container and all other containers are still synchronised. A resource the API
rejects is retried with an exponential backoff (30 s up to 30 min) instead of
on every event; changing its labels clears the backoff.

## Complete example

```yaml
services:
  app:
    image: ghcr.io/example/app:1.2.3
    networks: [npm]
    labels:
      # web UI with an API location block
      npm.proxy.domains: "app.example.com, www.app.example.com"
      npm.proxy.port: "8080"
      npm.proxy.location.0.path: "/api"
      npm.proxy.location.0.forward_port: "3000"

      # admin UI behind an access list, with forward auth
      npm.1.proxy.domains: "admin.app.example.com"
      npm.1.proxy.port: "9090"
      npm.1.proxy.access_list: "Intern"
      npm.1.proxy.auth_request: "authelia"
      npm.1.proxy.noindex: "true"

      # database stream
      npm.2.stream.incoming_port: "5432"

      # legacy domain
      npm.3.redirect.domains: "legacy.example.com"
      npm.3.redirect.forward_domain: "app.example.com"

      # parked domain
      npm.4.404.domains: "parked.example.com"

networks:
  npm:
    external: true
```

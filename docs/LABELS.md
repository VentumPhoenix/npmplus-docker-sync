# Labels

How labels are written and the rules behind them. **Every single label** —
what it does, its aliases, default and environment variable — is listed in
[FIELDS.md](FIELDS.md), generated from the table the parser reads.

- [Syntax](#syntax) · [Which containers](#which-containers-are-managed) · [Several resources](#several-resources-per-container) · [Defaults](#defaults)
- [Port](#upstream-port) · [Upstream host](#upstream-host) · [Certificates](#certificates) · [Access lists](#access-lists) · [Groups](#groups)
- [Custom locations](#custom-locations) · [Streams](#streams) · [NPMplus-only fields](#npmplus-only-and-retired-fields) · [Errors](#unknown-labels-and-validation) · [Full example](#full-example)

## Syntax

```
npm.<kind>.<field>             one resource (index 0)
npm.<index>.<kind>.<field>     several resources; npm.<kind>.<index>.<field> works too
npm.<field>                    shorthand for npm.proxy.<field>
npm.enable=false               ignore this container
npm.<index>.enable=false       ignore one index
```

| Part | Values |
|---|---|
| `npm` | the prefix, changeable with `LABEL_PREFIX`; `npm-proxy.domains` works too |
| `<kind>` | `proxy` (default), `redirect`, `stream`, `404` (alias `dead`) |
| `<index>` | `0`, `1`, `2`, … — defaults to `0`; `404` cannot be an index |
| `<field>` | see [FIELDS.md](FIELDS.md); `.`, `_` and `-` are interchangeable |

- Booleans: `true/false`, `1/0`, `yes/no`, `on/off`.
- Lists (domains, ids): separated by comma, semicolon or space.
- `ssl.hsts.subdomains`, `ssl.hsts_subdomains` and `ssl-hsts-subdomains` are the same field.

## Which containers are managed

Every container with at least one `npm.*` label. `npm.enable: "false"` opts one
out. With `NPM_EXPOSED_BY_DEFAULT=false` it is the other way round: only
containers with `npm.enable: "true"` are managed.

**`domains` is what visitors type, `host` is where the app runs** — as in
[Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync):

```yaml
npm.proxy.domains: "app.example.com"   # required
npm.proxy.host: "192.168.1.50"         # optional upstream, see below
```

For redirections and 404 hosts, which have no upstream, `host` means `domains`.

## Several resources per container

Each index is its own resource, and indexes may mix kinds:

```yaml
npm.proxy.domains: "app.example.com"            # 0: web UI
npm.proxy.port: "8080"
npm.1.proxy.domains: "metrics.app.example.com"  # 1: second port
npm.1.proxy.port: "9090"
npm.2.stream.incoming_port: "5432"              # 2: database
npm.3.redirect.domains: "old-app.example.com"   # 3: old domain
npm.3.redirect.forward_domain: "app.example.com"
npm.4.404.domains: "parked.example.com"         # 4: parked domain
```

## Defaults

For each field the first value found wins:

1. the label for that index — `npm.proxy.1.websockets`
2. the label without index — `npm.proxy.websockets`
3. `NPM_<KIND>_<FIELD>` on the sync container — `NPM_PROXY_WEBSOCKETS`
4. `NPM_DEFAULT_<FIELD>` — `NPM_DEFAULT_WEBSOCKETS`
5. the built-in default

So one variable sets a default for everything, and a label overrides it for one
container:

```yaml
NPM_PROXY_SSL_FORCE: "true"     # on the sync container: HTTPS everywhere ...
npm.proxy.ssl.force: "false"    # ... except on this container
```

Every alias has its own variable, which is why Redth's `NPM_PROXY_SSL_FORCE`
works unchanged. The built-in defaults make one label enough: upstream port
from the image, matching certificate, HTTPS redirect, HTTP/2 and HTTP/3.

## Upstream port

`port` (`forward_port`) is the port **inside** the container. Default `auto`:

- one exposed TCP port → that one,
- several → the first match in `NPM_PORT_PREFERENCE` (`80,8080,3000,8000,443`),
- none of them → the resource is skipped, and the log names the ports found.

## Upstream host

NPM must reach the container, so the upstream is its **IP address**, chosen in
this order:

1. an explicit `host` (`forward_host`) label;
2. with `NPM_NETWORK` set: the IP **in that network only**. A container not
   attached to it is skipped with a warning — any other address would end in a
   silent `502`. (`NPM_NETWORK_STRICT=false` restores the old fall-through.)
3. with `NPM_CONTAINER_NAME` set: the networks of the NPM container;
4. otherwise: a network the sync container shares, else the first network
   alphabetically, else the container name.

Only IPv4 is used. When a recreated container gets a new IP, its host follows
within seconds. `RESOLVE_CONTAINER_IP=false` (or `npm.proxy.resolve_ip: "false"`)
uses container names instead.

## Certificates

| `certificate` | Meaning |
|---|---|
| *(unset)* / `auto` | the best matching certificate (`NPM_DEFAULT_CERTIFICATE`) |
| `12` | that certificate id |
| `app.example.com`, `*.home.example.com` | a certificate covering this domain |
| `name:My wildcard` | by its name in NPM |
| `new` | request a Let's Encrypt certificate |
| `none` | plain HTTP |

**How `auto` chooses:** client CAs, deleted and expired certificates are
ignored. The rest rank *exact* → *exact plus more names* → *partly wildcard* →
*wildcard*, then by the longest validity. A wildcard covers exactly one level:
`*.home.example.com` matches `a.home.example.com`, not `home.example.com`. A
certificate that already fits is kept unless a strictly better one appears, so
importing a new wildcard does not shuffle hosts around. New certificates from
the NPM UI are noticed within `CERTIFICATE_POLL_INTERVAL` (1 minute).

When no certificate covers every domain, `NPM_CERTIFICATE_PARTIAL` decides:
`primary` (default) uses one for the first domain, `none` attaches nothing.
`NPM_CERTIFICATE_AUTO_CREATE=true` requests one instead (off by default:
Let's Encrypt rate limits).

**Requesting one:**

```yaml
npm.proxy.certificate: "new"
npm.letsencrypt.email: "admin@example.com"   # upstream NPM only -
npm.letsencrypt.agree: "true"                # NPMplus uses its ACME_EMAIL
# DNS-01, required for wildcards:
npm.letsencrypt.dns_challenge: "true"
npm.letsencrypt.dns_provider: "cloudflare"
npm.letsencrypt.dns_credentials: "dns_cloudflare_api_token=..."
```

> [!CAUTION]
> Anyone who can run `docker inspect` sees credentials in labels. Prefer
> creating such certificates once in the NPM UI — `auto` will find them.

**TLS cascade:** the server drops TLS settings that cannot apply, and the tool
does the same before comparing, so hosts converge:

```
no certificate  →  no ssl.forced, no HTTP/2, no HTTP/3
no ssl.forced   →  no HSTS
no HSTS         →  no HSTS subdomains
```

`ssl.forced` defaults to `auto`: on as soon as a certificate is attached.

## Access lists

By id or by name, resolved on every run:

```yaml
npm.proxy.access_list: "Intern, VPN"
npm.proxy.access_list_type: "public"   # remove them again
```

An unknown name skips the host with a warning — a typo never makes a protected
host public. Upstream NPM allows one list per host; more is refused before the
request. On NPMplus, custom locations default to `global` (inherit the host's).

## Groups

NPMplus can sort its host lists into groups:

```yaml
npm.proxy.group: "Production"   # aliases: directory, folder, category
NPM_DEFAULT_GROUP: "Docker"     # on the sync container: one group for everything
```

| Labels say | Result |
|---|---|
| nothing | the current group is kept — including one picked in the UI |
| `group: Production` | moved there, and back if someone moves it in the UI |
| `group: none` | taken out of its group (also opts out of `NPM_DEFAULT_GROUP`) |

Names are limited to 255 characters. Upstream NPM has no groups; the label is
left out there and reported once.

## Custom locations

```yaml
npm.proxy.location.0.path: "/api"
npm.proxy.location.0.forward_port: "3000"
npm.proxy.location.1.path: "/static"
npm.proxy.location.1.type: "prefer"        # nginx "^~"
npm.proxy.location.1.fancyindex: "true"
```

`type`: `prefix` (default), `exact` (`=`), `regex` (`~`), `iregex` (`~*`),
`prefer` (`^~`), `named` (`@`). Everything not set is inherited from the host.

## Streams

A stream is identified by its **incoming port** — one stream per port.

```yaml
npm.stream.incoming_port: "5432"
npm.stream.forward_port: "5432"
npm.stream.ssl: "db.example.com"         # certificate by domain
npm.1.stream.incoming_port: "51820"      # WireGuard
npm.1.stream.protocol: "udp"
```

`description` defaults to the container name. NPM only listens on ports its own
container publishes — publish the port on the NPM container first.

## NPMplus-only and retired fields

**NPMplus only:** `group`, `ssl.http3`, `noindex`, `crowdsec_appsec`,
`request_buffering`, `response_buffering`, `upstream_compression`,
`fancyindex`, `x_frame_options`, `auth_request`, `auth_request_upstream`,
`location_config`, `access_list_type`, and for streams `proxy_protocol`,
`proxy_tls`, `advanced_config`, `description`. Against upstream NPM they are
left out and reported once — nothing fails.

The labels are positive even where the API is inverted:
`crowdsec_appsec: "false"` turns AppSec off.

**Retired by newer NPMplus releases:** `websockets` and `ssl.http2` are always
on, `caching` and `block_exploits` no longer exist, `trust_forwarded_proto` is
always on. Such a server simply does not get them; an explicit label produces a
warning. Which fields a server accepts is read from the server itself.

## Unknown labels and validation

A misspelled label gets a suggestion and is otherwise ignored:

```
unknown label npm.proxy.ssl.forcd - did you mean npm.proxy.ssl.forced?
```

With `STRICT_LABELS=true` the resource is skipped instead. A resource is also
skipped — with the label named — when a required field is missing (`domains`,
`forward_domain`, `incoming_port`, `location.<n>.path`), a value does not parse
(port, number, boolean, enum, domain, status code `300–308`), a stream has
neither TCP nor UDP, `ssl.forced=true` meets `certificate: none`, or an access
list name is unknown. Other resources are not affected, and **a container with
broken labels keeps its hosts** — see [DELETION.md](DELETION.md).

A resource the API rejects is retried with growing pauses (30 s up to 30 min);
changing its labels retries it at once. `enabled: "false"` is applied through
NPM's enable/disable endpoints right after creating the host.

## Full example

```yaml
services:
  app:
    image: ghcr.io/example/app:1.2.3
    networks: [npm]
    labels:
      npm.proxy.domains: "app.example.com, www.app.example.com"
      npm.proxy.port: "8080"
      npm.proxy.group: "Production"
      npm.proxy.location.0.path: "/api"
      npm.proxy.location.0.forward_port: "3000"

      npm.1.proxy.domains: "admin.app.example.com"
      npm.1.proxy.port: "9090"
      npm.1.proxy.access_list: "Intern"
      npm.1.proxy.auth_request: "authelia"

      npm.2.stream.incoming_port: "5432"

      npm.3.redirect.domains: "legacy.example.com"
      npm.3.redirect.forward_domain: "app.example.com"

networks:
  npm:
    external: true
```

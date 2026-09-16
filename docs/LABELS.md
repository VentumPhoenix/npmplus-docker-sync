# Label reference

## Grammar

```
<prefix>.enable                          opt-in for the whole container (required)
<prefix>[.<index>].<kind>.<field>        one resource
<prefix>[.<index>].<field>               shorthand, <kind> defaults to proxy
<prefix>.<index>.enable=false            switch a single index off
```

| Part | Values | Notes |
|---|---|---|
| `<prefix>` | `npm` by default | Change it with `LABEL_PREFIX`. |
| `<index>` | any non-negative integer | Defaults to `0`; groups labels into one resource. |
| `<kind>` | `proxy`, `redirect` (`redirection`), `stream`, `404` (`dead`) | Defaults to `proxy`. |

Booleans accept `true/false`, `1/0`, `yes/no`, `on/off`. Domain lists are
comma, semicolon or whitespace separated, lower-cased and de-duplicated.

A container is only considered when `<prefix>.enable` is truthy. Everything
else is ignored, including containers with a partially filled label set.

> [!NOTE]
> `404` is reserved as a resource type, so it cannot be used as an index.
> Write `npm.404.host` (index 0, 404 host) or `npm.5.404.host` (index 5).

## Multiple resources per container

Each index is an independent resource, and indexes may mix resource types:

```yaml
labels:
  npm.enable: "true"

  npm.0.proxy.host: "app.example.com"
  npm.0.proxy.port: "8080"

  npm.1.proxy.host: "metrics.app.example.com"
  npm.1.proxy.port: "9090"

  npm.2.stream.incoming_port: "5432"

  npm.3.redirect.host: "old-app.example.com"
  npm.3.redirect.forward_domain: "app.example.com"

  npm.4.404.host: "parked.example.com"
```

An index may even carry two different kinds at once — for example a proxy host
and a stream that belong to the same service:

```yaml
npm.0.proxy.host: "minio.example.com"
npm.0.proxy.port: "9001"
npm.0.stream.incoming_port: "9000"
```

Disable one index without deleting its labels:

```yaml
npm.1.enable: "false"
```

## Proxy hosts — `npm.<i>.proxy.*`

### Required

| Field | Example | Description |
|---|---|---|
| `host` | `app.example.com, www.example.com` | Domain name(s). Aliases: `domain`, `domains`. |
| `port` | `8080` | Port **inside** the container, not a published host port. |

### Upstream

| Field | Default | Notes |
|---|---|---|
| `scheme` | `http` | `https` when the container itself serves TLS. |
| `forward_host` | container IP | Explicit upstream. Aliases: `forward_ip`, `upstream`. |
| `resolve_ip` | `RESOLVE_CONTAINER_IP` | `false` falls back to the container name. |

See [Upstream host resolution](#upstream-host-resolution) below.

### Behaviour

| Field | Default | Notes |
|---|---|---|
| `websockets` | `true` | Websocket upgrades. |
| `block_exploits` | `true` | NPM's common-exploit rules. |
| `caching` | `false` | Cache static assets. |
| `trust_forwarded_proto` | `false` | Trust the client's `X-Forwarded-Proto`. |
| `access_list_ids` | – | Ids of existing access lists, comma separated (aliases `access_list`, `access_lists`). NPMplus accepts several, upstream NPM exactly one. |
| `access_list_id` | – | Shorthand for a single id. |
| `access_list_type` | derived | `public` (no list) or `custom`; derived from the ids unless set. |
| `advanced_config` | – | Raw nginx directives for this host. |
| `enabled` | `true` | `false` creates the host and then disables it (see below). |

```yaml
npm.proxy.advanced_config: |
  client_max_body_size 0;
  proxy_read_timeout 600s;
```

### Custom location blocks

| Field | Default | Notes |
|---|---|---|
| `location.<n>.path` | — | **Required** per block, must start with `/`. |
| `location.<n>.forward_scheme` | host's scheme | `http` or `https`. |
| `location.<n>.forward_host` | host's upstream | Where this path goes. |
| `location.<n>.forward_port` | host's port | Upstream port. |
| `location.<n>.advanced_config` | – | Raw nginx directives inside the block. |

```yaml
npm.enable: "true"
npm.proxy.host: "app.example.com"
npm.proxy.port: "8080"

npm.proxy.location.0.path: "/api"
npm.proxy.location.0.forward_host: "api-backend"
npm.proxy.location.0.forward_port: "3000"

npm.proxy.location.1.path: "/ws"
npm.proxy.location.1.advanced_config: "proxy_read_timeout 86400s;"
```

Blocks are ordered by `<n>`; gaps are allowed.

## Redirection hosts — `npm.<i>.redirect.*`

| Field | Default | Notes |
|---|---|---|
| `host` | — | **Required.** Source domain(s). Alias: `from`. |
| `forward_domain` | — | **Required.** Target domain without scheme. Aliases: `to`, `target`, `forward_domain_name`. |
| `http_code` | `301` | 300–308. Aliases: `code`, `forward_http_code`. |
| `scheme` | `auto` | `auto` keeps the incoming scheme. |
| `preserve_path` | `true` | Append the request path to the target. |
| `block_exploits` | `true` | NPM's common-exploit rules. |
| `advanced_config` | – | Raw nginx directives. |
| `enabled` | `true` | Enable the redirect. |

```yaml
npm.enable: "true"
npm.redirect.from: "old-domain.example.com, www.old-domain.example.com"
npm.redirect.to: "app.example.com"
npm.redirect.code: "308"
npm.redirect.preserve_path: "true"
```

Plus the TLS fields below — a redirect that should answer on HTTPS needs its
own certificate.

## Streams — `npm.<i>.stream.*`

Streams have no domain names: their identity is the **incoming port**, so NPM
allows exactly one stream per port.

| Field | Default | Notes |
|---|---|---|
| `incoming_port` | — | **Required.** Port NPM listens on. Aliases: `port`, `listen_port`. |
| `forward_port` | = incoming port | Upstream port. Alias: `forwarding_port`. |
| `forward_host` | container IP | Upstream host. Aliases: `forwarding_host`, `forward_ip`. |
| `tcp` | `true` | Forward TCP. Alias: `tcp_forwarding`. |
| `udp` | `false` | Forward UDP. Alias: `udp_forwarding`. |
| `protocol` | `tcp` | Shorthand for both flags: `tcp`, `udp`, `both`. |
| `certificate_id` | – | For TLS-terminating streams. |
| `enabled` | `true` | Enable the stream. |

```yaml
# PostgreSQL over TCP
npm.enable: "true"
npm.stream.incoming_port: "5432"
npm.stream.forward_port: "5432"

# WireGuard over UDP, on a second index
npm.1.stream.incoming_port: "51820"
npm.1.stream.protocol: "udp"
```

Setting both `tcp` and `udp` to `false` is rejected: NPM would have nothing to
forward.

> [!IMPORTANT]
> NPM only accepts stream ports that its own container publishes. Publish the
> port on the NPM container first, otherwise the stream exists in the database
> but nothing listens.

## 404 hosts — `npm.<i>.404.*` (or `npm.<i>.dead.*`)

Park a domain on NPM's 404 page, e.g. to answer for a wildcard DNS record
without proxying anywhere.

| Field | Default | Notes |
|---|---|---|
| `host` | — | **Required.** Domain name(s). |
| `advanced_config` | – | Raw nginx directives. |
| `enabled` | `true` | Enable the host. |

```yaml
npm.enable: "true"
npm.404.host: "parked.example.com, *.parked.example.com"
npm.404.certificate_id: "7"
npm.404.ssl.forced: "true"
```

## TLS — every kind

| Field | Default | Notes |
|---|---|---|
| `certificate_id` | – | Existing id, or `new` for Let's Encrypt (aliases `le`, `letsencrypt`). |
| `ssl.forced` | `false` | HTTP → HTTPS redirect. Requires a certificate. |
| `ssl.http2` | `false` | HTTP/2. |
| `ssl.http3` | `false` | HTTP/3. **NPMplus only** — refused against upstream NPM. |
| `ssl.hsts` | `false` | HSTS header. |
| `ssl.hsts_subdomains` | `false` | `includeSubDomains`. |
| `letsencrypt.email` | – | Required with `certificate_id=new`. |
| `letsencrypt.agree` | `false` | Must be `true` with `certificate_id=new`. |
| `letsencrypt.dns_challenge` | `false` | DNS-01 instead of HTTP-01. |
| `letsencrypt.dns_provider` | – | Required with `dns_challenge=true`. |
| `letsencrypt.dns_credentials` | – | Provider credentials. |
| `letsencrypt.propagation_seconds` | `0` | DNS propagation wait. |

The server silently drops settings that cannot apply, and so does this tool
before comparing state — otherwise the two would never agree and every event
would trigger another update:

```
no certificate      -> ssl.forced         = false
no ssl.forced       -> ssl.hsts           = false
no ssl.hsts         -> ssl.hsts_subdomains = false
```

Using an existing certificate (its id is in the NPM URL when you edit it):

```yaml
npm.proxy.certificate_id: "7"
npm.proxy.ssl.forced: "true"
npm.proxy.ssl.http2: "true"
```

Requesting a new Let's Encrypt certificate:

```yaml
npm.proxy.certificate_id: "new"
npm.letsencrypt.email: "admin@example.com"
npm.letsencrypt.agree: "true"
```

> [!WARNING]
> Let's Encrypt enforces rate limits (50 certificates per registered domain per
> week). The certificate id NPM assigns is adopted on the next reconcile, so a
> certificate is requested only once — but test with `DRY_RUN=true` first.

DNS-01 challenge (required for wildcards):

```yaml
npm.proxy.host: "*.example.com"
npm.proxy.certificate_id: "new"
npm.letsencrypt.email: "admin@example.com"
npm.letsencrypt.agree: "true"
npm.letsencrypt.dns_challenge: "true"
npm.letsencrypt.dns_provider: "cloudflare"
npm.letsencrypt.dns_credentials: "dns_cloudflare_api_token=..."
npm.letsencrypt.propagation_seconds: "60"
```

> [!CAUTION]
> DNS credentials in labels are visible to anyone who can run
> `docker inspect`. Prefer creating such certificates once in the NPM UI and
> referencing them with `certificate_id`.

## The `enabled` flag

`enabled` is the one field that does **not** travel in the create/update body:
both APIs reject the property in their schemas and expose dedicated endpoints
instead (`POST <collection>/{id}/enable` and `/disable`). Every resource is
therefore created in the enabled state and switched afterwards if the label
says otherwise.

The reconcile loop compares the label with the live state and calls the
endpoint only when they differ, so:

* `enabled=false` takes effect on the run after the resource is created,
* toggling it in the NPM UI is corrected on the next reconcile,
* and it never triggers a `PUT`, which is what used to make a disabled host
  loop forever between "update" and "still not disabled".

## Access lists

NPMplus replaced NPM's single `access_list_id` with a list plus an explicit
type. Both spellings are accepted and mapped to whatever the detected flavour
wants:

```yaml
npm.proxy.access_list_ids: "2,5"      # NPMplus
npm.proxy.access_list_id: "2"         # either; shorthand for access_list_ids
npm.proxy.access_list_type: "public"  # drop the lists again
```

Custom locations carry their own access list on NPMplus, where the fields are
mandatory. The default is `global`, which means "inherit the proxy host's":

```yaml
npm.proxy.location.0.path: "/admin"
npm.proxy.location.0.access_list_ids: "4"    # -> type "custom"
npm.proxy.location.1.path: "/public"
npm.proxy.location.1.access_list_type: "public"
```

Passing more than one id to an upstream NPM server is refused before the
request, with a message saying so.

## Upstream host resolution

The upstream of proxy hosts and streams is resolved in this order:

1. the explicit `forward_host` label,
2. with `NPM_NETWORK` set: the container's IP **in that network only**. A
   container that is not attached to it is skipped with a warning naming the
   networks it is on — an address from elsewhere, and the container name
   alike, would be unreachable for NPM and yield a silent `502`.
   `NPM_NETWORK_STRICT=false` restores the fall-through of earlier releases.
3. without `NPM_NETWORK`: the container's IP in a network the sync container
   is attached to,
4. the container's IP in the first network (alphabetical order),
5. the container name.

Only IPv4 addresses are used — NPM writes the value straight into `proxy_pass`,
where a bare IPv6 address would be invalid. Containers without any IPv4
address (for example with `network_mode: host`) fall back to the name.

Disable the behaviour globally with `RESOLVE_CONTAINER_IP=false`, or per
resource:

```yaml
npm.proxy.resolve_ip: "false"      # use the container name for this host
```

## Validation

A resource is rejected (and the reason logged with the offending label) when:

* a required field is missing (`host`/`port`, `forward_domain`,
  `incoming_port`, `location.<n>.path`),
* a port is not a number in `1–65535`,
* a scheme is not `http`/`https` (or `auto` for redirects),
* a domain contains a path, space, scheme or port,
* a boolean or numeric label cannot be parsed,
* a redirect status code is outside `300–308`,
* a stream has neither TCP nor UDP enabled,
* `ssl.forced` is set without a certificate,
* `certificate_id=new` is used without `letsencrypt.email` and
  `letsencrypt.agree=true`,
* `letsencrypt.dns_challenge=true` is set without `letsencrypt.dns_provider`.

Invalid definitions are skipped individually: the other indexes of the same
container and all other containers are still synchronised.

## Complete example

```yaml
services:
  app:
    image: ghcr.io/example/app:1.2.3
    networks: [npm]
    labels:
      npm.enable: "true"

      # web UI with an API location block
      npm.0.proxy.host: "app.example.com, www.app.example.com"
      npm.0.proxy.port: "8080"
      npm.0.proxy.websockets: "true"
      npm.0.proxy.certificate_id: "7"
      npm.0.proxy.ssl.forced: "true"
      npm.0.proxy.ssl.http2: "true"
      npm.0.proxy.location.0.path: "/api"
      npm.0.proxy.location.0.forward_port: "3000"

      # admin UI behind an access list
      npm.1.proxy.host: "admin.app.example.com"
      npm.1.proxy.port: "9090"
      npm.1.proxy.access_list_ids: "2"

      # database stream
      npm.2.stream.incoming_port: "5432"

      # legacy domain
      npm.3.redirect.host: "legacy.example.com"
      npm.3.redirect.forward_domain: "app.example.com"

      # parked domain
      npm.4.404.host: "parked.example.com"

networks:
  npm:
    external: true
```

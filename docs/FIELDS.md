<!-- Generated from internal/fields; do not edit by hand. -->
<!-- Run "go test ./internal/fields -update" after changing the table. -->

# Field reference

Every label this tool understands, what it does, the environment variable that
changes its default and the NPM/NPMplus API field it ends up in. This page is
generated from the field table in `internal/fields`, which is the same table the
parser reads - it cannot be out of date. [LABELS.md](LABELS.md) explains the
grammar around these names.

A label is written `<prefix>.<kind>[.<index>].<field>`, so the `websockets` row
of the proxy table is the label `npm.proxy.websockets`.

* **Label** is the canonical spelling; **Aliases** are accepted just as well.
  Inside a name `.`, `_` and `-` are interchangeable, so
  `ssl.hsts_subdomains` and `ssl-hsts-subdomains` are the same field.
* **Type** is the value domain: `bool` takes `true/false`, `1/0`, `yes/no` or
  `on/off`; `domains` and `list` are comma, semicolon or whitespace
  separated; `port` is `1-65535`; an enum lists its values.
* **Default** is the built-in one. `auto` means it is derived at runtime: the
  certificate from the certificate list, the port from the container's exposed
  ports and `ssl.forced` from whether a certificate was attached. `—` means
  unset.
* **Environment** sets the default for every container. The kind specific name
  is shown; the cross-kind `NPM_DEFAULT_<FIELD>` works as well, and every
  alias has its own variable (which is how `NPM_PROXY_SSL_FORCE` works). A
  label on a container always wins.
* **+** after the label marks a field that only exists in NPMplus. Against
  upstream nginx-proxy-manager it is left out of the request and reported once,
  rather than failing the write.

## Resource kinds

`<kind>` selects which of NPM's four collections the labels describe. It may be
left out, in which case it is `proxy`.

| Kind | Spellings | NPM calls it |
|---|---|---|
| `proxy` | `proxy`, `proxies` | Proxy Host |
| `redirect` | `redirect`, `redirection` | Redirection Host |
| `stream` | `stream` | Stream |
| `dead` | `404`, `dead` | 404 Host |

## Labels that are not fields

These stand outside the per-resource tables below: they take no resource kind
and do not end up in any API field.

| Label | What it does |
|---|---|
| `npm.enable` | Whether the container is looked at at all. With `NPM_EXPOSED_BY_DEFAULT` (the default) every container carrying one label of the namespace is managed and `false` opts it out; with the flag off, `true` is required. |
| `npm.<index>.enable` | Switches one index off entirely, labels and all - nothing is created for it and nothing is deleted. Note the difference to `npm.<kind>.enabled` in the tables below, which *does* create the resource and leaves it disabled in NPM. |

## Proxy hosts (`proxy`)

| Label | Type | Default | What it does | Aliases | Environment | API field |
|---|---|---|---|---|---|---|
| `access_list` | list | — | access list ids or names, comma separated | `access_list_id`, `access_list_ids`, `access_lists`, `accesslist`, `accesslist.id`, `accesslist.ids` | `NPM_PROXY_ACCESS_LIST` | `npmplus_access_list_ids` |
| `access_list_type` **+** | `public`, `custom` | — | public or custom; derived from access_list when unset | `accesslist.type` | `NPM_PROXY_ACCESS_LIST_TYPE` | `npmplus_access_list_type` |
| `advanced_config` | string | — | nginx snippet inserted into the server block | `advanced`, `custom_config`, `advanced.config` | `NPM_PROXY_ADVANCED_CONFIG` | `advanced_config` |
| `auth_request` **+** | `none`, `anubis`, `tinyauth`, `oauth2proxy`, `voidauth`, `authelia`, `authentik`, `authentik-send-basic-auth` | `none` | forward authentication integration | `auth`, `forward_auth` | `NPM_PROXY_AUTH_REQUEST` | `npmplus_auth_request` |
| `auth_request_upstream` **+** | string | — | address of the auth service | `auth.upstream` | `NPM_PROXY_AUTH_REQUEST_UPSTREAM` | `npmplus_auth_request_upstream` |
| `block_exploits` | bool | `true` | block common exploit patterns | `block_common_exploits`, `exploits` | `NPM_PROXY_BLOCK_EXPLOITS` | `block_exploits` |
| `caching` | bool | `false` | cache static assets (can break dynamic content) | `cache`, `caching_enabled` | `NPM_PROXY_CACHING` | `caching_enabled` |
| `certificate` | certificate | `auto` | auto, a certificate id, a domain, `name:<nice name>`, `new` or `none` | `certificate_id`, `cert`, `ssl.certificate`, `ssl.certificate.id`, `ssl.cert` | `NPM_PROXY_CERTIFICATE` | `certificate_id` |
| `crowdsec_appsec` **+** | bool | `true` | run requests through the CrowdSec AppSec component | `crowdsec`, `appsec` | `NPM_PROXY_CROWDSEC_APPSEC` | `npmplus_crowdsec_appsec` |
| `domains` | domains | — | domain names served by this resource (required) | `domain`, `domain_names`, `domain_name` | `NPM_PROXY_DOMAINS` | `domain_names` |
| `enabled` | bool | `true` | switch this resource off in NPM without deleting it (create/update still runs) | `enable` | `NPM_PROXY_ENABLED` | `enabled` |
| `fancyindex` **+** | bool | `false` | pretty directory listings | `fancy_index` | `NPM_PROXY_FANCYINDEX` | `npmplus_fancyindex` |
| `forward_host` | string | — | upstream address; defaults to the container itself | `host`, `upstream`, `forward_ip`, `ip`, `target` | `NPM_PROXY_FORWARD_HOST` | `forward_host` |
| `forward_port` | port | `auto` | upstream port; auto reads it from the container's exposed ports | `port`, `upstream_port` | `NPM_PROXY_FORWARD_PORT` | `forward_port` |
| `forward_scheme` | `http`, `https`, `grpc`, `grpcs` | `http` | how to talk to the upstream | `scheme`, `upstream_scheme` | `NPM_PROXY_FORWARD_SCHEME` | `forward_scheme` |
| `group` **+** | string | — | NPMplus UI group this resource is listed under; none clears it | `directory`, `folder`, `category`, `ui.group` | `NPM_PROXY_GROUP` | `meta.directory` |
| `letsencrypt.agree` | bool | `false` | accept the Let's Encrypt terms (upstream NPM only) | `le.agree`, `certificate.agree` | `NPM_PROXY_LETSENCRYPT_AGREE` | `meta.letsencrypt_agree` |
| `letsencrypt.dns_challenge` | bool | `false` | use a DNS-01 challenge instead of HTTP-01 | `le.dns_challenge`, `dns_challenge` | `NPM_PROXY_LETSENCRYPT_DNS_CHALLENGE` | `meta.dns_challenge` |
| `letsencrypt.dns_credentials` | string | — | credentials file content for the DNS plugin | `le.dns_credentials`, `dns_credentials`, `letsencrypt.dns_provider_credentials` | `NPM_PROXY_LETSENCRYPT_DNS_CREDENTIALS` | `meta.dns_provider_credentials` |
| `letsencrypt.dns_provider` | string | — | certbot DNS plugin name | `le.dns_provider`, `dns_provider` | `NPM_PROXY_LETSENCRYPT_DNS_PROVIDER` | `meta.dns_provider` |
| `letsencrypt.email` | string | — | contact address (upstream NPM only; NPMplus uses its own ACME_EMAIL) | `le.email`, `certificate.email` | `NPM_PROXY_LETSENCRYPT_EMAIL` | `meta.letsencrypt_email` |
| `letsencrypt.propagation_seconds` | int | `0` | how long to wait for the DNS record to propagate | `le.propagation_seconds`, `propagation_seconds` | `NPM_PROXY_LETSENCRYPT_PROPAGATION_SECONDS` | `meta.propagation_seconds` |
| `location_config` **+** | string | — | nginx snippet inserted into location / | — | `NPM_PROXY_LOCATION_CONFIG` | `npmplus_location_config` |
| `noindex` **+** | bool | `false` | send X-Robots-Tag: noindex and block scrapers | `no_index` | `NPM_PROXY_NOINDEX` | `npmplus_noindex` |
| `request_buffering` **+** | bool | `true` | buffer the request body before passing it upstream | `proxy_request_buffering` | `NPM_PROXY_REQUEST_BUFFERING` | `npmplus_proxy_request_buffering` |
| `resolve_ip` | bool | — | use the container IP instead of its name as upstream; defaults to RESOLVE_CONTAINER_IP | `resolve_container_ip` | `NPM_PROXY_RESOLVE_IP` | `forward_host` |
| `response_buffering` **+** | bool | `true` | buffer the upstream response | `proxy_response_buffering` | `NPM_PROXY_RESPONSE_BUFFERING` | `npmplus_proxy_response_buffering` |
| `ssl.forced` | bool | `auto` | redirect http to https; auto means "on as soon as a certificate is attached" | `ssl.force`, `force_ssl`, `ssl_forced` | `NPM_PROXY_SSL_FORCED` | `ssl_forced` |
| `ssl.hsts` | bool | `false` | send Strict-Transport-Security (hard to undo, opt-in) | `hsts`, `hsts_enabled` | `NPM_PROXY_SSL_HSTS` | `hsts_enabled` |
| `ssl.hsts_subdomains` | bool | `false` | extend HSTS to all subdomains | `hsts_subdomains`, `hsts.sub` | `NPM_PROXY_SSL_HSTS_SUBDOMAINS` | `hsts_subdomains` |
| `ssl.http2` | bool | `true` | HTTP/2 support | `http2`, `http2_support` | `NPM_PROXY_SSL_HTTP2` | `http2_support` |
| `ssl.http3` **+** | bool | `true` | HTTP/3 (QUIC) support | `http3`, `http3_support`, `quic` | `NPM_PROXY_SSL_HTTP3` | `npmplus_http3_support` |
| `trust_forwarded_proto` | bool | `false` | trust X-Forwarded-Proto (only behind another proxy) | `trust_proto` | `NPM_PROXY_TRUST_FORWARDED_PROTO` | `trust_forwarded_proto` |
| `upstream_compression` **+** | bool | `false` | let the upstream compress instead of nginx | `compression` | `NPM_PROXY_UPSTREAM_COMPRESSION` | `npmplus_upstream_compression` |
| `websockets` | bool | `true` | allow websocket upgrades | `websocket`, `ws`, `allow_websocket_upgrade` | `NPM_PROXY_WEBSOCKETS` | `allow_websocket_upgrade` |
| `x_frame_options` **+** | `deny`, `sameorigin`, `upstream`, `none` | — | X-Frame-Options header; unset keeps the NPMplus default | `xframe_options`, `frame_options` | `NPM_PROXY_X_FRAME_OPTIONS` | `npmplus_x_frame_options` |

## Redirection hosts (`redirect`)

| Label | Type | Default | What it does | Aliases | Environment | API field |
|---|---|---|---|---|---|---|
| `advanced_config` | string | — | nginx snippet inserted into the server block | `advanced`, `custom_config`, `advanced.config` | `NPM_REDIRECT_ADVANCED_CONFIG` | `advanced_config` |
| `block_exploits` | bool | `true` | block common exploit patterns | `block_common_exploits`, `exploits` | `NPM_REDIRECT_BLOCK_EXPLOITS` | `block_exploits` |
| `certificate` | certificate | `auto` | auto, a certificate id, a domain, `name:<nice name>`, `new` or `none` | `certificate_id`, `cert`, `ssl.certificate`, `ssl.certificate.id`, `ssl.cert` | `NPM_REDIRECT_CERTIFICATE` | `certificate_id` |
| `domains` | domains | — | domain names served by this resource (required) | `domain`, `domain_names`, `domain_name`, `host`, `hosts` | `NPM_REDIRECT_DOMAINS` | `domain_names` |
| `enabled` | bool | `true` | switch this resource off in NPM without deleting it (create/update still runs) | `enable` | `NPM_REDIRECT_ENABLED` | `enabled` |
| `forward_domain` | string | — | domain to redirect to (required) | `forward_domain_name`, `to`, `target`, `destination` | `NPM_REDIRECT_FORWARD_DOMAIN` | `forward_domain_name` |
| `forward_scheme` | `auto`, `http`, `https` | `auto` | scheme of the redirect target | `scheme` | `NPM_REDIRECT_FORWARD_SCHEME` | `forward_scheme` |
| `group` **+** | string | — | NPMplus UI group this resource is listed under; none clears it | `directory`, `folder`, `category`, `ui.group` | `NPM_REDIRECT_GROUP` | `meta.directory` |
| `http_code` | int | `301` | redirect status code (300-308) | `code`, `forward_http_code`, `status` | `NPM_REDIRECT_HTTP_CODE` | `forward_http_code` |
| `letsencrypt.agree` | bool | `false` | accept the Let's Encrypt terms (upstream NPM only) | `le.agree`, `certificate.agree` | `NPM_REDIRECT_LETSENCRYPT_AGREE` | `meta.letsencrypt_agree` |
| `letsencrypt.dns_challenge` | bool | `false` | use a DNS-01 challenge instead of HTTP-01 | `le.dns_challenge`, `dns_challenge` | `NPM_REDIRECT_LETSENCRYPT_DNS_CHALLENGE` | `meta.dns_challenge` |
| `letsencrypt.dns_credentials` | string | — | credentials file content for the DNS plugin | `le.dns_credentials`, `dns_credentials`, `letsencrypt.dns_provider_credentials` | `NPM_REDIRECT_LETSENCRYPT_DNS_CREDENTIALS` | `meta.dns_provider_credentials` |
| `letsencrypt.dns_provider` | string | — | certbot DNS plugin name | `le.dns_provider`, `dns_provider` | `NPM_REDIRECT_LETSENCRYPT_DNS_PROVIDER` | `meta.dns_provider` |
| `letsencrypt.email` | string | — | contact address (upstream NPM only; NPMplus uses its own ACME_EMAIL) | `le.email`, `certificate.email` | `NPM_REDIRECT_LETSENCRYPT_EMAIL` | `meta.letsencrypt_email` |
| `letsencrypt.propagation_seconds` | int | `0` | how long to wait for the DNS record to propagate | `le.propagation_seconds`, `propagation_seconds` | `NPM_REDIRECT_LETSENCRYPT_PROPAGATION_SECONDS` | `meta.propagation_seconds` |
| `preserve_path` | bool | `true` | append the original path to the target | `preserve` | `NPM_REDIRECT_PRESERVE_PATH` | `preserve_path` |
| `ssl.forced` | bool | `auto` | redirect http to https; auto means "on as soon as a certificate is attached" | `ssl.force`, `force_ssl`, `ssl_forced` | `NPM_REDIRECT_SSL_FORCED` | `ssl_forced` |
| `ssl.hsts` | bool | `false` | send Strict-Transport-Security (hard to undo, opt-in) | `hsts`, `hsts_enabled` | `NPM_REDIRECT_SSL_HSTS` | `hsts_enabled` |
| `ssl.hsts_subdomains` | bool | `false` | extend HSTS to all subdomains | `hsts_subdomains`, `hsts.sub` | `NPM_REDIRECT_SSL_HSTS_SUBDOMAINS` | `hsts_subdomains` |
| `ssl.http2` | bool | `true` | HTTP/2 support | `http2`, `http2_support` | `NPM_REDIRECT_SSL_HTTP2` | `http2_support` |
| `ssl.http3` **+** | bool | `true` | HTTP/3 (QUIC) support | `http3`, `http3_support`, `quic` | `NPM_REDIRECT_SSL_HTTP3` | `npmplus_http3_support` |

## Streams (`stream`)

| Label | Type | Default | What it does | Aliases | Environment | API field |
|---|---|---|---|---|---|---|
| `advanced_config` **+** | string | — | nginx snippet inserted into the stream block | `advanced`, `custom_config`, `advanced.config` | `NPM_STREAM_ADVANCED_CONFIG` | `npmplus_advanced_config` |
| `certificate` | certificate | `auto` | auto, a certificate id, a domain or `name:<nice name>` | `certificate_id`, `cert`, `ssl`, `ssl.certificate`, `ssl.certificate.id` | `NPM_STREAM_CERTIFICATE` | `certificate_id` |
| `description` **+** | string | — | free text shown in the UI; defaults to the container name | `comment`, `note` | `NPM_STREAM_DESCRIPTION` | `npmplus_description` |
| `enabled` | bool | `true` | switch this resource off in NPM without deleting it (create/update still runs) | `enable` | `NPM_STREAM_ENABLED` | `enabled` |
| `forward_host` | string | — | upstream address; defaults to the container itself | `host`, `forward.host`, `forwarding_host`, `upstream`, `forward_ip`, `ip`, `target` | `NPM_STREAM_FORWARD_HOST` | `forwarding_host` |
| `forward_port` | port | — | upstream port; defaults to the incoming port | `forward.port`, `forwarding_port` | `NPM_STREAM_FORWARD_PORT` | `forwarding_port` |
| `group` **+** | string | — | NPMplus UI group this resource is listed under; none clears it | `directory`, `folder`, `category`, `ui.group` | `NPM_STREAM_GROUP` | `meta.directory` |
| `incoming_port` | port | — | port NPM listens on (required) | `port`, `listen_port`, `incoming.port`, `listen` | `NPM_STREAM_INCOMING_PORT` | `incoming_port` |
| `protocol` | `tcp`, `udp`, `both` | — | shorthand for the tcp and udp switches: tcp, udp or both | — | `NPM_STREAM_PROTOCOL` | `tcp_forwarding/udp_forwarding` |
| `proxy_protocol` **+** | `off`, `v1`, `v2` | `off` | send a PROXY protocol header upstream | `proxy_protocol_forwarding` | `NPM_STREAM_PROXY_PROTOCOL` | `npmplus_proxy_protocol_forwarding` |
| `proxy_tls` **+** | bool | `false` | terminate TLS towards the upstream | `proxy_ssl` | `NPM_STREAM_PROXY_TLS` | `npmplus_proxy_tls` |
| `resolve_ip` | bool | — | use the container IP as upstream; defaults to RESOLVE_CONTAINER_IP | `resolve_container_ip` | `NPM_STREAM_RESOLVE_IP` | `forwarding_host` |
| `tcp` | bool | `true` | forward TCP | `forward.tcp`, `tcp_forwarding` | `NPM_STREAM_TCP` | `tcp_forwarding` |
| `udp` | bool | `false` | forward UDP | `forward.udp`, `udp_forwarding` | `NPM_STREAM_UDP` | `udp_forwarding` |

## 404 hosts (`dead`)

| Label | Type | Default | What it does | Aliases | Environment | API field |
|---|---|---|---|---|---|---|
| `advanced_config` | string | — | nginx snippet inserted into the server block | `advanced`, `custom_config`, `advanced.config` | `NPM_DEAD_ADVANCED_CONFIG` | `advanced_config` |
| `certificate` | certificate | `auto` | auto, a certificate id, a domain, `name:<nice name>`, `new` or `none` | `certificate_id`, `cert`, `ssl.certificate`, `ssl.certificate.id`, `ssl.cert` | `NPM_DEAD_CERTIFICATE` | `certificate_id` |
| `domains` | domains | — | domain names served by this resource (required) | `domain`, `domain_names`, `domain_name`, `host`, `hosts` | `NPM_DEAD_DOMAINS` | `domain_names` |
| `enabled` | bool | `true` | switch this resource off in NPM without deleting it (create/update still runs) | `enable` | `NPM_DEAD_ENABLED` | `enabled` |
| `group` **+** | string | — | NPMplus UI group this resource is listed under; none clears it | `directory`, `folder`, `category`, `ui.group` | `NPM_DEAD_GROUP` | `meta.directory` |
| `letsencrypt.agree` | bool | `false` | accept the Let's Encrypt terms (upstream NPM only) | `le.agree`, `certificate.agree` | `NPM_DEAD_LETSENCRYPT_AGREE` | `meta.letsencrypt_agree` |
| `letsencrypt.dns_challenge` | bool | `false` | use a DNS-01 challenge instead of HTTP-01 | `le.dns_challenge`, `dns_challenge` | `NPM_DEAD_LETSENCRYPT_DNS_CHALLENGE` | `meta.dns_challenge` |
| `letsencrypt.dns_credentials` | string | — | credentials file content for the DNS plugin | `le.dns_credentials`, `dns_credentials`, `letsencrypt.dns_provider_credentials` | `NPM_DEAD_LETSENCRYPT_DNS_CREDENTIALS` | `meta.dns_provider_credentials` |
| `letsencrypt.dns_provider` | string | — | certbot DNS plugin name | `le.dns_provider`, `dns_provider` | `NPM_DEAD_LETSENCRYPT_DNS_PROVIDER` | `meta.dns_provider` |
| `letsencrypt.email` | string | — | contact address (upstream NPM only; NPMplus uses its own ACME_EMAIL) | `le.email`, `certificate.email` | `NPM_DEAD_LETSENCRYPT_EMAIL` | `meta.letsencrypt_email` |
| `letsencrypt.propagation_seconds` | int | `0` | how long to wait for the DNS record to propagate | `le.propagation_seconds`, `propagation_seconds` | `NPM_DEAD_LETSENCRYPT_PROPAGATION_SECONDS` | `meta.propagation_seconds` |
| `ssl.forced` | bool | `auto` | redirect http to https; auto means "on as soon as a certificate is attached" | `ssl.force`, `force_ssl`, `ssl_forced` | `NPM_DEAD_SSL_FORCED` | `ssl_forced` |
| `ssl.hsts` | bool | `false` | send Strict-Transport-Security (hard to undo, opt-in) | `hsts`, `hsts_enabled` | `NPM_DEAD_SSL_HSTS` | `hsts_enabled` |
| `ssl.hsts_subdomains` | bool | `false` | extend HSTS to all subdomains | `hsts_subdomains`, `hsts.sub` | `NPM_DEAD_SSL_HSTS_SUBDOMAINS` | `hsts_subdomains` |
| `ssl.http2` | bool | `true` | HTTP/2 support | `http2`, `http2_support` | `NPM_DEAD_SSL_HTTP2` | `http2_support` |
| `ssl.http3` **+** | bool | `true` | HTTP/3 (QUIC) support | `http3`, `http3_support`, `quic` | `NPM_DEAD_SSL_HTTP3` | `npmplus_http3_support` |

## Custom locations (`location.<n>.<field>`)

Location blocks belong to a proxy host and inherit every switch from it.

| Label | Type | Default | What it does | Aliases | API field |
|---|---|---|---|---|---|
| `path` | string | — | location path, e.g. /api (required) | — | `path` |
| `type` **+** | `prefix`, `exact`, `regex`, `iregex`, `prefer`, `named` | `prefix` | nginx location modifier: prefix, exact (`= `), regex (`~ `), iregex (`~* `), prefer (`^~ `) or named (`@`) | `location_type`, `modifier` | `location_type` |
| `forward_host` | string | — | upstream address for this path; inherited from the host | `host`, `upstream` | `forward_host` |
| `forward_port` | port | — | upstream port for this path; inherited from the host | `port` | `forward_port` |
| `forward_scheme` | `http`, `https`, `grpc`, `grpcs` | — | how to talk to the upstream of this path; inherited from the host | `scheme` | `forward_scheme` |
| `advanced_config` | string | — | nginx snippet inserted for this location | `advanced`, `advanced.config` | `advanced_config` |
| `location_config` **+** | string | — | nginx snippet inserted inside the location block | — | `npmplus_location_config` |
| `access_list` **+** | list | — | access list ids or names for this path only | `access_list_id`, `access_list_ids`, `access_lists`, `accesslist` | `npmplus_access_list_ids` |
| `access_list_type` **+** | `global`, `public`, `custom` | — | global inherits the host's lists, public drops them, custom uses this block's | — | `npmplus_access_list_type` |
| `enabled` **+** | bool | `true` | switch off this one location without removing its labels | `enable` | `npmplus_enabled` |
| `noindex` **+** | bool | — | send X-Robots-Tag: noindex for this path; inherited from the host | — | `npmplus_noindex` |
| `crowdsec_appsec` **+** | bool | — | run this path through CrowdSec AppSec; inherited from the host | `crowdsec`, `appsec` | `npmplus_crowdsec_appsec` |
| `request_buffering` **+** | bool | — | buffer the request body for this path; inherited from the host | — | `npmplus_proxy_request_buffering` |
| `response_buffering` **+** | bool | — | buffer the upstream response for this path; inherited from the host | — | `npmplus_proxy_response_buffering` |
| `upstream_compression` **+** | bool | — | let the upstream compress this path; inherited from the host | `compression` | `npmplus_upstream_compression` |
| `fancyindex` **+** | bool | — | pretty directory listings for this path; inherited from the host | `fancy_index` | `npmplus_fancyindex` |
| `x_frame_options` **+** | `deny`, `sameorigin`, `upstream`, `none` | — | X-Frame-Options header for this path; inherited from the host | `xframe_options` | `npmplus_x_frame_options` |
| `auth_request` **+** | `none`, `anubis`, `tinyauth`, `oauth2proxy`, `voidauth`, `authelia`, `authentik`, `authentik-send-basic-auth` | — | forward authentication for this path; inherited from the host | `auth` | `npmplus_auth_request` |
| `auth_request_upstream` **+** | string | — | address of the auth service; inherited from the host | — | `npmplus_auth_request_upstream` |

## NPMplus-only fields

These exist only in NPMplus. Against upstream nginx-proxy-manager they are left
out of the request and reported once per resource - but only when a label set
them, never because of a default.

| Kind | Label | API field | What it does |
|---|---|---|---|
| `proxy` | `access_list_type` | `npmplus_access_list_type` | public or custom; derived from access_list when unset |
| `proxy` | `auth_request` | `npmplus_auth_request` | forward authentication integration |
| `proxy` | `auth_request_upstream` | `npmplus_auth_request_upstream` | address of the auth service |
| `proxy` | `crowdsec_appsec` | `npmplus_crowdsec_appsec` | run requests through the CrowdSec AppSec component |
| `proxy` | `fancyindex` | `npmplus_fancyindex` | pretty directory listings |
| `proxy` | `group` | `meta.directory` | NPMplus UI group this resource is listed under; none clears it |
| `proxy` | `location_config` | `npmplus_location_config` | nginx snippet inserted into location / |
| `proxy` | `noindex` | `npmplus_noindex` | send X-Robots-Tag: noindex and block scrapers |
| `proxy` | `request_buffering` | `npmplus_proxy_request_buffering` | buffer the request body before passing it upstream |
| `proxy` | `response_buffering` | `npmplus_proxy_response_buffering` | buffer the upstream response |
| `proxy` | `ssl.http3` | `npmplus_http3_support` | HTTP/3 (QUIC) support |
| `proxy` | `upstream_compression` | `npmplus_upstream_compression` | let the upstream compress instead of nginx |
| `proxy` | `x_frame_options` | `npmplus_x_frame_options` | X-Frame-Options header; unset keeps the NPMplus default |
| `redirect` | `group` | `meta.directory` | NPMplus UI group this resource is listed under; none clears it |
| `redirect` | `ssl.http3` | `npmplus_http3_support` | HTTP/3 (QUIC) support |
| `stream` | `advanced_config` | `npmplus_advanced_config` | nginx snippet inserted into the stream block |
| `stream` | `description` | `npmplus_description` | free text shown in the UI; defaults to the container name |
| `stream` | `group` | `meta.directory` | NPMplus UI group this resource is listed under; none clears it |
| `stream` | `proxy_protocol` | `npmplus_proxy_protocol_forwarding` | send a PROXY protocol header upstream |
| `stream` | `proxy_tls` | `npmplus_proxy_tls` | terminate TLS towards the upstream |
| `dead` | `group` | `meta.directory` | NPMplus UI group this resource is listed under; none clears it |
| `dead` | `ssl.http3` | `npmplus_http3_support` | HTTP/3 (QUIC) support |

## Inverted switches

NPMplus spells three settings as "disable X". The labels are positive, and the
value is negated on the way into the API:

| Label | API field | `true` means |
|---|---|---|
| `crowdsec_appsec` | `npmplus_crowdsec_appsec` | AppSec is **active** (the API field is set to `false`) |
| `request_buffering` | `npmplus_proxy_request_buffering` | buffering is **active** |
| `response_buffering` | `npmplus_proxy_response_buffering` | buffering is **active** |

The explicit spellings `disable_crowdsec_appsec`, `disable_request_buffering` and
`disable_response_buffering` are accepted too and mean the opposite.

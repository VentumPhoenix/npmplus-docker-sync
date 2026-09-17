<!-- Generated from internal/fields; do not edit by hand. -->
<!-- Run "go test ./internal/fields -update" after changing the table. -->

# Field reference

Every property of a managed resource, with the label that sets it, the
environment variable that changes its default and the NPM/NPMplus API field it
ends up in.

* **Label** is the canonical spelling. Inside a name `.`, `_` and `-` are
  interchangeable, so `ssl.hsts_subdomains` and `ssl-hsts-subdomains` are the
  same field.
* **Environment** sets the default for every container. The kind specific name
  is shown; the cross-kind `NPM_DEFAULT_<FIELD>` works as well, and every
  alias has its own variable (which is how `NPM_PROXY_SSL_FORCE` works).
* **Default** `auto` means the value is derived at runtime: the certificate
  from the certificate list, the port from the container's exposed ports and
  `ssl.forced` from whether a certificate was attached.
* **+** marks a field that only exists in NPMplus. Against upstream
  nginx-proxy-manager it is left out of the request and reported once.

## Proxy hosts (`proxy`)

| Label | Aliases | Type | Default | Environment | API field | |
|---|---|---|---|---|---|---|
| `access_list` | `access_list_id`, `access_list_ids`, `access_lists`, `accesslist`, `accesslist.id`, `accesslist.ids` | list | — | `NPM_PROXY_ACCESS_LIST` | `npmplus_access_list_ids` |  |
| `access_list_type` | `accesslist.type` | `public`, `custom` | — | `NPM_PROXY_ACCESS_LIST_TYPE` | `npmplus_access_list_type` | + |
| `advanced_config` | `advanced`, `custom_config`, `advanced.config` | string | — | `NPM_PROXY_ADVANCED_CONFIG` | `advanced_config` |  |
| `auth_request` | `auth`, `forward_auth` | `none`, `anubis`, `tinyauth`, `oauth2proxy`, `voidauth`, `authelia`, `authentik`, `authentik-send-basic-auth` | `none` | `NPM_PROXY_AUTH_REQUEST` | `npmplus_auth_request` | + |
| `auth_request_upstream` | `auth.upstream` | string | — | `NPM_PROXY_AUTH_REQUEST_UPSTREAM` | `npmplus_auth_request_upstream` | + |
| `block_exploits` | `block_common_exploits`, `exploits` | bool | `true` | `NPM_PROXY_BLOCK_EXPLOITS` | `block_exploits` |  |
| `caching` | `cache`, `caching_enabled` | bool | `false` | `NPM_PROXY_CACHING` | `caching_enabled` |  |
| `certificate` | `certificate_id`, `cert`, `ssl.certificate`, `ssl.certificate.id`, `ssl.cert` | certificate | `auto` | `NPM_PROXY_CERTIFICATE` | `certificate_id` |  |
| `crowdsec_appsec` | `crowdsec`, `appsec` | bool | `true` | `NPM_PROXY_CROWDSEC_APPSEC` | `npmplus_crowdsec_appsec` | + |
| `domains` | `domain`, `domain_names`, `domain_name` | domains | — | `NPM_PROXY_DOMAINS` | `domain_names` |  |
| `enabled` | `enable` | bool | `true` | `NPM_PROXY_ENABLED` | `enabled` |  |
| `fancyindex` | `fancy_index` | bool | `false` | `NPM_PROXY_FANCYINDEX` | `npmplus_fancyindex` | + |
| `forward_host` | `host`, `upstream`, `forward_ip`, `ip`, `target` | string | — | `NPM_PROXY_FORWARD_HOST` | `forward_host` |  |
| `forward_port` | `port`, `upstream_port` | port | `auto` | `NPM_PROXY_FORWARD_PORT` | `forward_port` |  |
| `forward_scheme` | `scheme`, `upstream_scheme` | `http`, `https`, `grpc`, `grpcs` | `http` | `NPM_PROXY_FORWARD_SCHEME` | `forward_scheme` |  |
| `letsencrypt.agree` | `le.agree`, `certificate.agree` | bool | `false` | `NPM_PROXY_LETSENCRYPT_AGREE` | `meta.letsencrypt_agree` |  |
| `letsencrypt.dns_challenge` | `le.dns_challenge`, `dns_challenge` | bool | `false` | `NPM_PROXY_LETSENCRYPT_DNS_CHALLENGE` | `meta.dns_challenge` |  |
| `letsencrypt.dns_credentials` | `le.dns_credentials`, `dns_credentials`, `letsencrypt.dns_provider_credentials` | string | — | `NPM_PROXY_LETSENCRYPT_DNS_CREDENTIALS` | `meta.dns_provider_credentials` |  |
| `letsencrypt.dns_provider` | `le.dns_provider`, `dns_provider` | string | — | `NPM_PROXY_LETSENCRYPT_DNS_PROVIDER` | `meta.dns_provider` |  |
| `letsencrypt.email` | `le.email`, `certificate.email` | string | — | `NPM_PROXY_LETSENCRYPT_EMAIL` | `meta.letsencrypt_email` |  |
| `letsencrypt.propagation_seconds` | `le.propagation_seconds`, `propagation_seconds` | int | `0` | `NPM_PROXY_LETSENCRYPT_PROPAGATION_SECONDS` | `meta.propagation_seconds` |  |
| `location_config` | — | string | — | `NPM_PROXY_LOCATION_CONFIG` | `npmplus_location_config` | + |
| `noindex` | `no_index` | bool | `false` | `NPM_PROXY_NOINDEX` | `npmplus_noindex` | + |
| `request_buffering` | `proxy_request_buffering` | bool | `true` | `NPM_PROXY_REQUEST_BUFFERING` | `npmplus_proxy_request_buffering` | + |
| `resolve_ip` | `resolve_container_ip` | bool | — | `NPM_PROXY_RESOLVE_IP` | `forward_host` |  |
| `response_buffering` | `proxy_response_buffering` | bool | `true` | `NPM_PROXY_RESPONSE_BUFFERING` | `npmplus_proxy_response_buffering` | + |
| `ssl.forced` | `ssl.force`, `force_ssl`, `ssl_forced` | bool | `auto` | `NPM_PROXY_SSL_FORCED` | `ssl_forced` |  |
| `ssl.hsts` | `hsts`, `hsts_enabled` | bool | `false` | `NPM_PROXY_SSL_HSTS` | `hsts_enabled` |  |
| `ssl.hsts_subdomains` | `hsts_subdomains`, `hsts.sub` | bool | `false` | `NPM_PROXY_SSL_HSTS_SUBDOMAINS` | `hsts_subdomains` |  |
| `ssl.http2` | `http2`, `http2_support` | bool | `true` | `NPM_PROXY_SSL_HTTP2` | `http2_support` |  |
| `ssl.http3` | `http3`, `http3_support`, `quic` | bool | `true` | `NPM_PROXY_SSL_HTTP3` | `npmplus_http3_support` | + |
| `trust_forwarded_proto` | `trust_proto` | bool | `false` | `NPM_PROXY_TRUST_FORWARDED_PROTO` | `trust_forwarded_proto` |  |
| `upstream_compression` | `compression` | bool | `false` | `NPM_PROXY_UPSTREAM_COMPRESSION` | `npmplus_upstream_compression` | + |
| `websockets` | `websocket`, `ws`, `allow_websocket_upgrade` | bool | `true` | `NPM_PROXY_WEBSOCKETS` | `allow_websocket_upgrade` |  |
| `x_frame_options` | `xframe_options`, `frame_options` | `deny`, `sameorigin`, `upstream`, `none` | — | `NPM_PROXY_X_FRAME_OPTIONS` | `npmplus_x_frame_options` | + |

## Redirection hosts (`redirect`)

| Label | Aliases | Type | Default | Environment | API field | |
|---|---|---|---|---|---|---|
| `advanced_config` | `advanced`, `custom_config`, `advanced.config` | string | — | `NPM_REDIRECT_ADVANCED_CONFIG` | `advanced_config` |  |
| `block_exploits` | `block_common_exploits`, `exploits` | bool | `true` | `NPM_REDIRECT_BLOCK_EXPLOITS` | `block_exploits` |  |
| `certificate` | `certificate_id`, `cert`, `ssl.certificate`, `ssl.certificate.id`, `ssl.cert` | certificate | `auto` | `NPM_REDIRECT_CERTIFICATE` | `certificate_id` |  |
| `domains` | `domain`, `domain_names`, `domain_name`, `host`, `hosts` | domains | — | `NPM_REDIRECT_DOMAINS` | `domain_names` |  |
| `enabled` | `enable` | bool | `true` | `NPM_REDIRECT_ENABLED` | `enabled` |  |
| `forward_domain` | `forward_domain_name`, `to`, `target`, `destination` | string | — | `NPM_REDIRECT_FORWARD_DOMAIN` | `forward_domain_name` |  |
| `forward_scheme` | `scheme` | `auto`, `http`, `https` | `auto` | `NPM_REDIRECT_FORWARD_SCHEME` | `forward_scheme` |  |
| `http_code` | `code`, `forward_http_code`, `status` | int | `301` | `NPM_REDIRECT_HTTP_CODE` | `forward_http_code` |  |
| `letsencrypt.agree` | `le.agree`, `certificate.agree` | bool | `false` | `NPM_REDIRECT_LETSENCRYPT_AGREE` | `meta.letsencrypt_agree` |  |
| `letsencrypt.dns_challenge` | `le.dns_challenge`, `dns_challenge` | bool | `false` | `NPM_REDIRECT_LETSENCRYPT_DNS_CHALLENGE` | `meta.dns_challenge` |  |
| `letsencrypt.dns_credentials` | `le.dns_credentials`, `dns_credentials`, `letsencrypt.dns_provider_credentials` | string | — | `NPM_REDIRECT_LETSENCRYPT_DNS_CREDENTIALS` | `meta.dns_provider_credentials` |  |
| `letsencrypt.dns_provider` | `le.dns_provider`, `dns_provider` | string | — | `NPM_REDIRECT_LETSENCRYPT_DNS_PROVIDER` | `meta.dns_provider` |  |
| `letsencrypt.email` | `le.email`, `certificate.email` | string | — | `NPM_REDIRECT_LETSENCRYPT_EMAIL` | `meta.letsencrypt_email` |  |
| `letsencrypt.propagation_seconds` | `le.propagation_seconds`, `propagation_seconds` | int | `0` | `NPM_REDIRECT_LETSENCRYPT_PROPAGATION_SECONDS` | `meta.propagation_seconds` |  |
| `preserve_path` | `preserve` | bool | `true` | `NPM_REDIRECT_PRESERVE_PATH` | `preserve_path` |  |
| `ssl.forced` | `ssl.force`, `force_ssl`, `ssl_forced` | bool | `auto` | `NPM_REDIRECT_SSL_FORCED` | `ssl_forced` |  |
| `ssl.hsts` | `hsts`, `hsts_enabled` | bool | `false` | `NPM_REDIRECT_SSL_HSTS` | `hsts_enabled` |  |
| `ssl.hsts_subdomains` | `hsts_subdomains`, `hsts.sub` | bool | `false` | `NPM_REDIRECT_SSL_HSTS_SUBDOMAINS` | `hsts_subdomains` |  |
| `ssl.http2` | `http2`, `http2_support` | bool | `true` | `NPM_REDIRECT_SSL_HTTP2` | `http2_support` |  |
| `ssl.http3` | `http3`, `http3_support`, `quic` | bool | `true` | `NPM_REDIRECT_SSL_HTTP3` | `npmplus_http3_support` | + |

## Streams (`stream`)

| Label | Aliases | Type | Default | Environment | API field | |
|---|---|---|---|---|---|---|
| `advanced_config` | `advanced`, `custom_config`, `advanced.config` | string | — | `NPM_STREAM_ADVANCED_CONFIG` | `npmplus_advanced_config` | + |
| `certificate` | `certificate_id`, `cert`, `ssl`, `ssl.certificate`, `ssl.certificate.id` | certificate | `auto` | `NPM_STREAM_CERTIFICATE` | `certificate_id` |  |
| `description` | `comment`, `note` | string | — | `NPM_STREAM_DESCRIPTION` | `npmplus_description` | + |
| `enabled` | `enable` | bool | `true` | `NPM_STREAM_ENABLED` | `enabled` |  |
| `forward_host` | `host`, `forward.host`, `forwarding_host`, `upstream`, `forward_ip`, `ip`, `target` | string | — | `NPM_STREAM_FORWARD_HOST` | `forwarding_host` |  |
| `forward_port` | `forward.port`, `forwarding_port` | port | — | `NPM_STREAM_FORWARD_PORT` | `forwarding_port` |  |
| `incoming_port` | `port`, `listen_port`, `incoming.port`, `listen` | port | — | `NPM_STREAM_INCOMING_PORT` | `incoming_port` |  |
| `protocol` | — | `tcp`, `udp`, `both` | — | `NPM_STREAM_PROTOCOL` | `tcp_forwarding/udp_forwarding` |  |
| `proxy_protocol` | `proxy_protocol_forwarding` | `off`, `v1`, `v2` | `off` | `NPM_STREAM_PROXY_PROTOCOL` | `npmplus_proxy_protocol_forwarding` | + |
| `proxy_tls` | `proxy_ssl` | bool | `false` | `NPM_STREAM_PROXY_TLS` | `npmplus_proxy_tls` | + |
| `resolve_ip` | `resolve_container_ip` | bool | — | `NPM_STREAM_RESOLVE_IP` | `forwarding_host` |  |
| `tcp` | `forward.tcp`, `tcp_forwarding` | bool | `true` | `NPM_STREAM_TCP` | `tcp_forwarding` |  |
| `udp` | `forward.udp`, `udp_forwarding` | bool | `false` | `NPM_STREAM_UDP` | `udp_forwarding` |  |

## 404 hosts (`dead`)

| Label | Aliases | Type | Default | Environment | API field | |
|---|---|---|---|---|---|---|
| `advanced_config` | `advanced`, `custom_config`, `advanced.config` | string | — | `NPM_DEAD_ADVANCED_CONFIG` | `advanced_config` |  |
| `certificate` | `certificate_id`, `cert`, `ssl.certificate`, `ssl.certificate.id`, `ssl.cert` | certificate | `auto` | `NPM_DEAD_CERTIFICATE` | `certificate_id` |  |
| `domains` | `domain`, `domain_names`, `domain_name`, `host`, `hosts` | domains | — | `NPM_DEAD_DOMAINS` | `domain_names` |  |
| `enabled` | `enable` | bool | `true` | `NPM_DEAD_ENABLED` | `enabled` |  |
| `letsencrypt.agree` | `le.agree`, `certificate.agree` | bool | `false` | `NPM_DEAD_LETSENCRYPT_AGREE` | `meta.letsencrypt_agree` |  |
| `letsencrypt.dns_challenge` | `le.dns_challenge`, `dns_challenge` | bool | `false` | `NPM_DEAD_LETSENCRYPT_DNS_CHALLENGE` | `meta.dns_challenge` |  |
| `letsencrypt.dns_credentials` | `le.dns_credentials`, `dns_credentials`, `letsencrypt.dns_provider_credentials` | string | — | `NPM_DEAD_LETSENCRYPT_DNS_CREDENTIALS` | `meta.dns_provider_credentials` |  |
| `letsencrypt.dns_provider` | `le.dns_provider`, `dns_provider` | string | — | `NPM_DEAD_LETSENCRYPT_DNS_PROVIDER` | `meta.dns_provider` |  |
| `letsencrypt.email` | `le.email`, `certificate.email` | string | — | `NPM_DEAD_LETSENCRYPT_EMAIL` | `meta.letsencrypt_email` |  |
| `letsencrypt.propagation_seconds` | `le.propagation_seconds`, `propagation_seconds` | int | `0` | `NPM_DEAD_LETSENCRYPT_PROPAGATION_SECONDS` | `meta.propagation_seconds` |  |
| `ssl.forced` | `ssl.force`, `force_ssl`, `ssl_forced` | bool | `auto` | `NPM_DEAD_SSL_FORCED` | `ssl_forced` |  |
| `ssl.hsts` | `hsts`, `hsts_enabled` | bool | `false` | `NPM_DEAD_SSL_HSTS` | `hsts_enabled` |  |
| `ssl.hsts_subdomains` | `hsts_subdomains`, `hsts.sub` | bool | `false` | `NPM_DEAD_SSL_HSTS_SUBDOMAINS` | `hsts_subdomains` |  |
| `ssl.http2` | `http2`, `http2_support` | bool | `true` | `NPM_DEAD_SSL_HTTP2` | `http2_support` |  |
| `ssl.http3` | `http3`, `http3_support`, `quic` | bool | `true` | `NPM_DEAD_SSL_HTTP3` | `npmplus_http3_support` | + |

## Custom locations (`location.<n>.<field>`)

Location blocks belong to a proxy host and inherit every switch from it.

| Label | Aliases | Type | Default | API field | |
|---|---|---|---|---|---|
| `path` | — | string | — | `path` |  |
| `type` | `location_type`, `modifier` | `prefix`, `exact`, `regex`, `iregex`, `prefer`, `named` | `prefix` | `location_type` | + |
| `forward_host` | `host`, `upstream` | string | — | `forward_host` |  |
| `forward_port` | `port` | port | — | `forward_port` |  |
| `forward_scheme` | `scheme` | `http`, `https`, `grpc`, `grpcs` | — | `forward_scheme` |  |
| `advanced_config` | `advanced`, `advanced.config` | string | — | `advanced_config` |  |
| `location_config` | — | string | — | `npmplus_location_config` | + |
| `access_list` | `access_list_id`, `access_list_ids`, `access_lists`, `accesslist` | list | — | `npmplus_access_list_ids` | + |
| `access_list_type` | — | `global`, `public`, `custom` | — | `npmplus_access_list_type` | + |
| `enabled` | `enable` | bool | `true` | `npmplus_enabled` | + |
| `noindex` | — | bool | — | `npmplus_noindex` | + |
| `crowdsec_appsec` | `crowdsec`, `appsec` | bool | — | `npmplus_crowdsec_appsec` | + |
| `request_buffering` | — | bool | — | `npmplus_proxy_request_buffering` | + |
| `response_buffering` | — | bool | — | `npmplus_proxy_response_buffering` | + |
| `upstream_compression` | `compression` | bool | — | `npmplus_upstream_compression` | + |
| `fancyindex` | `fancy_index` | bool | — | `npmplus_fancyindex` | + |
| `x_frame_options` | `xframe_options` | `deny`, `sameorigin`, `upstream`, `none` | — | `npmplus_x_frame_options` | + |
| `auth_request` | `auth` | `none`, `anubis`, `tinyauth`, `oauth2proxy`, `voidauth`, `authelia`, `authentik`, `authentik-send-basic-auth` | — | `npmplus_auth_request` | + |
| `auth_request_upstream` | — | string | — | `npmplus_auth_request_upstream` | + |

## NPMplus-only fields

These exist only in NPMplus. Against upstream nginx-proxy-manager they are left
out of the request and reported once per resource - but only when a label set
them, never because of a default.

| Kind | Label | API field |
|---|---|---|
| `proxy` | `access_list_type` | `npmplus_access_list_type` |
| `proxy` | `auth_request` | `npmplus_auth_request` |
| `proxy` | `auth_request_upstream` | `npmplus_auth_request_upstream` |
| `proxy` | `crowdsec_appsec` | `npmplus_crowdsec_appsec` |
| `proxy` | `fancyindex` | `npmplus_fancyindex` |
| `proxy` | `location_config` | `npmplus_location_config` |
| `proxy` | `noindex` | `npmplus_noindex` |
| `proxy` | `request_buffering` | `npmplus_proxy_request_buffering` |
| `proxy` | `response_buffering` | `npmplus_proxy_response_buffering` |
| `proxy` | `ssl.http3` | `npmplus_http3_support` |
| `proxy` | `upstream_compression` | `npmplus_upstream_compression` |
| `proxy` | `x_frame_options` | `npmplus_x_frame_options` |
| `redirect` | `ssl.http3` | `npmplus_http3_support` |
| `stream` | `advanced_config` | `npmplus_advanced_config` |
| `stream` | `description` | `npmplus_description` |
| `stream` | `proxy_protocol` | `npmplus_proxy_protocol_forwarding` |
| `stream` | `proxy_tls` | `npmplus_proxy_tls` |
| `dead` | `ssl.http3` | `npmplus_http3_support` |

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

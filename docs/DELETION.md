# When does something get deleted?

This tool removes resources from NPM. That is the whole point of orphan
cleanup — and the one thing that can hurt. This page lists every situation in
which a deletion happens, and every safeguard that stops one.

## The short answer

A resource is deleted only when **all** of these are true:

1. it carries our ownership marker (`managed_by: npmplus-docker-sync`) **and**
   this instance's id,
2. no container claims its identity any more,
3. `DELETE_ORPHANS=true` (the default),
4. the run is not the shutdown flush,
5. the container that owned it is not *protected* (see below),
6. the delete guard does not consider the run implausible.

Everything else is left alone.

## Situations

| Situation | What happens |
|---|---|
| A container is removed (`docker rm`, `docker compose down`) | Its resources become orphans and are deleted. |
| A container is stopped or crash-looping | `NPM_ON_STOP` decides: `disable` (default), `keep` or `delete`. The host keeps its id either way. |
| A container is restarted or recreated | Nothing at all happens inside `NPM_STOP_GRACE` (default 1 minute). |
| A label has a typo | The container is **protected**: its resources are kept, and a warning names the label. |
| `STRICT_LABELS=true` and an unknown label | Same protection — the resource is skipped, not deleted. |
| `LABEL_PREFIX` changed | Deletions are refused for the whole run, with the old prefix named. |
| `NPM_EXPOSED_BY_DEFAULT` switched to `false` | Every unlabelled container becomes an orphan at once, which the delete guard stops. |
| Docker or the socket proxy answers with an empty container list | Deletions are refused: "no containers at all" is never a reason to delete. |
| Another sync instance manages the host | Never touched (`SYNC_INSTANCE_ID`). Without an id - `/info` denied and nothing configured - instance scoping is off and every resource with our marker counts as ours, which is what a single instance wants. |
| Another tool manages the host (`npm-docker-sync`, anything else) | Never touched. |
| The host was created by hand | Adopted when a label claims its domain, otherwise never touched — and never deleted while unmanaged. |
| SIGTERM / container shutdown | The final flush creates and updates, but never deletes. |

## Protected containers

If the labels of a container cannot be parsed — `npm.proxy.port: "80a"`, an
invalid enum value, a domain that is not one — that container yields no
resources for the run. Without a safeguard its hosts would look orphaned and be
deleted, so a typo would take a production host down.

Instead the container is recorded as protected, and every resource whose meta
names it survives the run:

```
WARN ignoring invalid label definition (its hosts are protected from deletion)
     error="container web: npm.proxy.port: \"80a\" is not a number"
WARN keeping the resource of a container whose labels could not be read
     key=web.example.com id=42 container=web
```

Fix the label and the next run reconciles normally.

## The delete guard

`DELETE_GUARD` (default `0.5`) is the largest share of the managed resources a
single run may delete. A run that would exceed it — or that would delete *all*
of them, or that saw no containers at all — is stopped and reported:

```
ERROR refusing to delete: this would delete every managed resource (12)
      would_delete=12 managed=12 containers=0 resources=proxy/app.example.com#7,...
```

The guard only applies from `DELETE_GUARD_MIN` deletions upwards (default 3),
so a two-host setup can still tear itself down. `DELETE_GUARD=off` disables it
entirely.

The reason is also visible in `/readyz` and `/status`, and counted as
`npmsync_deletions_blocked_total` in `/metrics`.

## Before you switch anything on

Run once with `DRY_RUN=true`. Every deletion is announced and nothing is
written:

```
[dry-run] would delete orphaned resource key=npm.example.com id=64
```

That is also the right way to check the one case the guard cannot judge for
you: a host this tool created during an earlier experiment keeps its marker for
good, so once its labels are gone it *is* an orphan — even when it is the page
you manage NPM with. Give those containers their labels back.

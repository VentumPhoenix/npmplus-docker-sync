# When is something deleted?

Removing hosts whose container is gone is the point of this tool — and the one
thing that can hurt. This page lists when it happens and what prevents it.

## The rule

A host is deleted only when **all** of this is true:

1. it is **ours**: it carries our marker line (`# npmplus-docker-sync: …` in its
   advanced config) or `managed_by: npmplus-docker-sync` in its meta object —
   and belongs to this instance,
2. no container claims its domain (or port) any more,
3. `DELETE_ORPHANS=true` (default),
4. it is not the last sync during shutdown,
5. its container is not *protected* (broken labels, see below),
6. the delete guard does not stop the run.

Everything else is left alone.

## Situations

| Situation | What happens |
|---|---|
| Container removed | Its hosts are deleted. |
| Container stopped or crash-looping | `NPM_ON_STOP`: `disable` (default), `keep` or `delete`. The host keeps its id. |
| Container restarted or recreated | Nothing, within `NPM_STOP_GRACE` (1 minute). |
| Typo in a label | The container is **protected**: its hosts stay, a warning names the label. |
| `LABEL_PREFIX` changed | No deletions at all in that run; the old prefix is named. |
| Docker reports no containers | No deletions — that is never a reason to delete. |
| Too many deletions at once | Refused by the delete guard, see below. |
| Host made by hand | Never deleted. Adopted if a label claims its domain. |
| Host of another tool or instance | Never touched. |
| Marker copied to another host, or domain edited by hand | That host is nobody's: never deleted, adopted if a label claims it. |
| NPMplus update empties the meta object | Nothing changes — the marker keeps the owner. Hosts that never got a marker lose their owner and are then treated as hand-made. |
| SIGTERM / shutdown | The last sync creates and updates, but never deletes. |

## Protected containers

If a container's labels cannot be parsed (`npm.proxy.port: "80a"`), it yields
no hosts for that run. Without protection its hosts would look orphaned and be
deleted. Instead they are kept:

```
WARN ignoring invalid label definition (its hosts are protected from deletion)
WARN keeping the resource of a container whose labels could not be read  key=web.example.com
```

Fix the label and the next sync works normally.

## The delete guard

`DELETE_GUARD` (default `0.5`) is the largest share of our hosts one run may
delete. A run that would exceed it, delete *all* of them, or that saw no
containers at all is stopped:

```
ERROR refusing to delete: this would delete every managed resource (12)
```

The guard starts at `DELETE_GUARD_MIN` deletions (3), so a small setup can still
clean up. `DELETE_GUARD=off` disables it. A refusal shows up in `/readyz`,
`/status` and the metric `npmsync_deletions_blocked_total`.

## Before the first real run

Run once with `DRY_RUN=true` — every deletion is announced, nothing is written:

```
[dry-run] would delete orphaned resource key=npm.example.com id=64
```

That catches the one case no rule can judge: a host this tool created in an
earlier test stays ours, so once its labels are gone it *is* an orphan — even
if it is the page you manage NPM with. Give such containers their labels back.

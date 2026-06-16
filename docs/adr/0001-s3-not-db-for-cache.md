# 1. S3, not a database, for the Cache Store

Date: 2026-06-16
Status: Accepted

## Context

The Cache (`cache.FileCache[T]`, see CONTEXT.md) is moving from local files to a
storage Store seam with an S3 adapter (replacing the bulk `entrypoint.sh` sync). The
question arose whether the Store's durable adapter should be S3 or a database
(DynamoDB / RDS Postgres).

The Cache's access pattern is narrow and fixed:

- Operations are **key → blob only**: `Get(key)`, `Put(key)`, `Remove(key)`, occasional
  clear. No queries, scans, joins, secondary indexes, or aggregation.
- Values are JSON envelopes, small to ~321 KB (the largest today is
  `fangraphs-pit-*`); some may exceed 400 KB.
- Data is **regenerable and TTL-evicted** — a miss just re-fetches upstream.
- Writers are effectively single per key; concurrent same-key writes are benign
  (identical data, last-write-wins).

## Decision

The Cache Store uses **S3** (per-key `GetObject`/`PutObject`), not a database.

- S3 matches the key→blob shape exactly (it is already JSON files), has no item-size
  ceiling, costs pennies at this volume, and needs no server or connection management
  from short-lived Fargate tasks.
- **DynamoDB** has a 400 KB item cap that the largest blobs approach or exceed, forcing
  an S3 offload anyway; its native TTL is redundant because `FileCache` already does TTL
  via the envelope.
- **RDS/Postgres** is a running, paid server whose relational power is wasted on
  key→blob, and connection handling from ephemeral tasks is friction.

Heuristic: **key-lookup → S3; query/aggregate → DB.** The Cache only ever does
key-lookup.

## Consequences

- The s3 adapter (`internal/cachestore/s3`) is a thin `GetObject`/`PutObject`/`DeleteObject`
  wrapper; no schema, table, or migration work.
- TTL stays in `FileCache`'s envelope; the Store remains a dumb byte layer.
- This decision is **scoped to the Cache only**. The future **Analysis Store**
  (durable, queryable history — snapshots, claims ledger) has a different access pattern
  (`SELECT … WHERE date/team/player`) and re-opens the S3+Athena-vs-DB question on its own
  terms. This ADR does not pre-decide that.
- A database for the Cache should not be re-proposed without a new access pattern that
  needs queries.

# Versioning Policy

Flotilla follows [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).
This document is a public commitment: what version numbers mean, what counts
as breaking, and how we deprecate. Notable changes are recorded in
CHANGELOG.md (created with the first release).

## Public surfaces

A MAJOR bump (post-1.0) is triggered by a breaking change to **any** of:

1. **`flt` CLI** — documented commands, flags and their semantics.
2. **Adapter JSON schema** — the definition format community adapters are
   written against.
3. **Daemon IPC protocol** — the named-pipe JSON-RPC contract between GUI,
   daemon and session hosts, including the token handshake.
4. **On-disk state** — the SQLite layout and session files a newer build must
   open from an older build of the same MAJOR line (forward compatibility is
   not guaranteed).

Explicitly **not** public: internal package APIs, the Electron IPC surface,
anything marked experimental.

## Pre-1.0 (the 0.x line)

Plain SemVer allows anything in 0.x; we commit to a stricter rule:

- **Breaking changes and new functionality** bump the **MINOR** (0.1.x → 0.2.0).
- **Fix-only releases** bump the **PATCH** (0.1.0 → 0.1.1).
- A MINOR bump alone does not mean something broke; every breaking change is
  called out in the CHANGELOG under Changed/Removed with a migration note.

The adapter JSON schema and the IPC protocol are **experimental** until 1.0:
they may change in any MINOR, with the change written down. They earn a
compatibility promise once community adapters and external clients ship
against them.

1.0.0 ships when the four surfaces are stable enough to promise MAJOR-line
compatibility — realistically after the M2 orchestrator lands and the adapter
schema has survived community use.

## Deprecation

- Deprecate before removing: a deprecated surface keeps working and says so
  for at least one MINOR release.
- On-disk state migrates forward automatically on open, with the format
  version recorded so the migration is deterministic and one-way within a
  MAJOR line.
- Removals and migrations are always documented in the CHANGELOG.

## Releases & tags

- Tags are `vMAJOR.MINOR.PATCH` on `main`, created only by the release flow
  ([RELEASING.md](RELEASING.md)) after an explicit request.
- Two channels coexist: **stable** (`flt`) and **preview**
  (`flt-preview`, SemVer pre-release suffix `-preview.N`). Preview builds are
  not covered by the compatibility promises above.
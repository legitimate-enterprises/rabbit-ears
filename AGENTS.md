@./README.md

- `server/` and `web/` each own their ecosystem's tooling; the root
  `Makefile` fans the standard targets out to them. The other clients are
  repositories of their own; each tests against the `v<version>` tag its
  copy of the spec names, so a released tag is never moved.
- Red-green: write the failing test first, then the minimum code. Unit,
  integration and, where reasonable, end-to-end tests are all first class.
  Judge coverage by the defects caught at seams: API boundaries, ffmpeg
  invocations, database queries, metadata providers, client capability
  parsing. Integration tests run real wiring against `fixtures/` media.
- Comments explain intent, invariants and side effects of non-obvious code.
  Never what the code does, never history or rationale.
- No Markdown beyond `README.md`, `AGENTS.md` and `memory.md`. No ADRs, no
  decision logs.
- Server architecture is service-oriented: domain models, services, HTTP
  handlers in separate packages. Every domain model gets a migration, a
  repository, and full CRUD in the REST API by default.
- Migrations are forward-only and numbered contiguously from
  `0001_baseline.sql`.
- Row ids are UUIDv7 (`uid.ID`): 16-byte BLOBs in SQLite, lowercase strings
  in the API. A table with an id has `created_at` and `modified_at`, Unix ms
  set in Go; a table people create rows in also has `created_by` and
  `modified_by`, and its create operations accept a client-chosen id.
- The server's version is SemVer, minor for a feature and patch for a fix,
  and is the API's: `info.version` in the spec states it, a breaking API
  change takes a new versioned path, and clients may assume it.
- Store temporal values as UTC instants. Model relationally; no JSON columns.

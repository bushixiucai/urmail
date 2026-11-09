# CI status snapshot — 2026-11-10 (v0.6.20 campaign Phase 0, B1)

Baseline verification of the five CI optimization items, recorded while the dev
environment moved to Debian 13. All items are already merged on main.

| Item | Status | Evidence |
|---|---|---|
| paths filter (docs/go/frontend/deps) | in service | docs-only PRs #99 / #101 completed in 0.2 min (Detect changed paths + Shell only) |
| Integration split into 4 parallel shards, one PostgreSQL each | in service | run 42633118150: handlers 10.4 / data 9.0 / platform 11.1 / core 10.9 min |
| Go caches (gomod, GOCACHE, shared tools) | in service | cache steps in `.github/workflows/ci.yaml`; Go job 4.9 min |
| shared toolchain actions (go.dev / nodejs.org, not GitHub CDN) | in service | PR #56; `.github/actions/setup-go-toolchain`, `.github/actions/setup-node-toolchain` |
| concurrency cancels superseded runs | in service | run 42632937973 cancelled by a newer main push after 1.4 min |

Slowest job on the latest main run: Integration (platform) at 11.1 min; a full
main run is ≈13 min wall clock including queueing. The pre-#50 figures
(Integration 30–37 min, docs-only PRs blocked behind the full pipeline) no
longer occur on this pipeline.

Recorded by urMail DevOps during Phase 0.

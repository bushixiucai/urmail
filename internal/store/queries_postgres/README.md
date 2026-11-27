# PostgreSQL query source snapshot

These 90 `.sql` files are the PostgreSQL sqlc inputs copied from repository `main` at `5c6e6a7359547112860508d5b1d28704da4dbef5` for B1 dual-output generation. They are deliberately separate from `internal/store/queries`, whose SQL has been converted for the SQLite sqlc engine.

`sqlc.postgres.yaml` generates this snapshot to `internal/store/generated/postgres`. The SQLite configuration generates `internal/store/queries` to `internal/store/generated/sqlite`. B1 verifies and compiles both output packages but does not change the running service's query assembly; runtime selection/adaptation is B2.

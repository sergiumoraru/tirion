package graph

import (
	"context"
	"crypto/sha256"
	"fmt"
)

// Schema is the baseline for both existing unversioned databases and fresh installs.
// Once shipped, keep applied migrations immutable and append subsequent changes.
var schemaMigrations = []string{Schema, `
-- Pending-call consumers use exact/prefix caller IDs or the separately indexed
-- function component. No query searches substrings of the full caller ID.
-- Keep those B-tree/component indexes and the callee-name trigram index.
DROP INDEX IF EXISTS idx_pending_calls_caller_trgm;
`}

func (s *Storage) migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize first-time adoption and upgrades across CLI/server processes. The
	// migration and its receipt commit together; failed upgrades remain retryable.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(741928,0)`); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('tirion_schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec(ctx, `CREATE TABLE tirion_schema_migrations (
		 version integer PRIMARY KEY, checksum text NOT NULL,
		 applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT version,checksum FROM tirion_schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	applied := 0
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return err
		}
		if version != applied+1 || version > len(schemaMigrations) {
			rows.Close()
			return fmt.Errorf("unsupported database migration history at version %d; use a compatible Tirion binary", version)
		}
		if checksum != fmt.Sprintf("%x", sha256.Sum256([]byte(schemaMigrations[version-1]))) {
			rows.Close()
			return fmt.Errorf("database migration %d checksum differs; applied migrations must not be edited", version)
		}
		applied++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := applied; i < len(schemaMigrations); i++ {
		if _, err := tx.Exec(ctx, schemaMigrations[i]); err != nil {
			return fmt.Errorf("apply database migration %d: %w", i+1, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(schemaMigrations[i])))
		if _, err := tx.Exec(ctx, `INSERT INTO tirion_schema_migrations(version,checksum) VALUES($1,$2)`, i+1, checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

package main

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sergiumoraru/tirion/internal/graph"
)

type ibmiModuleState struct {
	used          bool
	schemaEnsured bool
}

func (s *ibmiModuleState) markUsed() {
	if s == nil {
		return
	}
	s.used = true
}

func (s *ibmiModuleState) ensureSchema(storage *graph.Storage) error {
	if s == nil || s.schemaEnsured {
		return nil
	}
	if _, err := storage.Pool().Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS ibmi_exports (
		  id SERIAL PRIMARY KEY,
		  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
		  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
		  file_id INTEGER REFERENCES files(id) ON DELETE CASCADE,
		  function_id INTEGER REFERENCES functions(id) ON DELETE CASCADE,
		  object_name TEXT,
		  object_type TEXT,
		  export_name TEXT NOT NULL,
		  source_type TEXT NOT NULL,
		  line_number INTEGER
		);

		CREATE TABLE IF NOT EXISTS ibmi_bindings (
		  id SERIAL PRIMARY KEY,
		  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
		  snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE,
		  file_id INTEGER REFERENCES files(id) ON DELETE CASCADE,
		  owner_object TEXT,
		  binding_name TEXT NOT NULL,
		  binding_type TEXT NOT NULL,
		  target_object TEXT,
		  target_object_type TEXT,
		  line_number INTEGER
		);

		ALTER TABLE ibmi_exports ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;
		ALTER TABLE ibmi_bindings ADD COLUMN IF NOT EXISTS snapshot_id BIGINT REFERENCES repo_snapshots(id) ON DELETE CASCADE;

		DO $$
		DECLARE constraint_name text;
		BEGIN
		  FOR constraint_name IN
		    SELECT conname FROM pg_constraint WHERE conrelid = 'ibmi_exports'::regclass AND contype = 'u'
		  LOOP
		    EXECUTE format('ALTER TABLE ibmi_exports DROP CONSTRAINT IF EXISTS %I', constraint_name);
		  END LOOP;
		  FOR constraint_name IN
		    SELECT conname FROM pg_constraint WHERE conrelid = 'ibmi_bindings'::regclass AND contype = 'u'
		  LOOP
		    EXECUTE format('ALTER TABLE ibmi_bindings DROP CONSTRAINT IF EXISTS %I', constraint_name);
		  END LOOP;
		END $$;

		CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_exports_snapshot_unique
		  ON ibmi_exports(snapshot_id, repo_id, file_id, function_id, export_name, object_name, source_type, line_number)
		  WHERE snapshot_id IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_exports_legacy_unique
		  ON ibmi_exports(repo_id, file_id, function_id, export_name, object_name, source_type, line_number)
		  WHERE snapshot_id IS NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_bindings_snapshot_unique
		  ON ibmi_bindings(snapshot_id, repo_id, file_id, owner_object, binding_name, binding_type, target_object, target_object_type, line_number)
		  WHERE snapshot_id IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_ibmi_bindings_legacy_unique
		  ON ibmi_bindings(repo_id, file_id, owner_object, binding_name, binding_type, target_object, target_object_type, line_number)
		  WHERE snapshot_id IS NULL;
		CREATE INDEX IF NOT EXISTS idx_ibmi_exports_repo ON ibmi_exports(repo_id);
		CREATE INDEX IF NOT EXISTS idx_ibmi_exports_snapshot ON ibmi_exports(snapshot_id);
		CREATE INDEX IF NOT EXISTS idx_ibmi_exports_name ON ibmi_exports(export_name);
		CREATE INDEX IF NOT EXISTS idx_ibmi_exports_object ON ibmi_exports(object_name);
		CREATE INDEX IF NOT EXISTS idx_ibmi_exports_function ON ibmi_exports(function_id);
		CREATE INDEX IF NOT EXISTS idx_ibmi_bindings_repo ON ibmi_bindings(repo_id);
		CREATE INDEX IF NOT EXISTS idx_ibmi_bindings_snapshot ON ibmi_bindings(snapshot_id);
		CREATE INDEX IF NOT EXISTS idx_ibmi_bindings_name ON ibmi_bindings(binding_name);
		CREATE INDEX IF NOT EXISTS idx_ibmi_bindings_target ON ibmi_bindings(target_object);
	`); err != nil {
		return err
	}
	s.schemaEnsured = true
	return nil
}

func (s *ibmiModuleState) resolveRepo(repoCtx *repoFinalizeContext) error {
	if s == nil || !s.used || repoCtx == nil || repoCtx.storage == nil {
		return nil
	}
	repoID := repoCtx.repoID
	storage := repoCtx.storage
	activeSnapshotIDs := make([]int64, 0, len(repoCtx.activeSnapshotIDs)+1)
	if repoCtx.snapshotID != nil {
		activeSnapshotIDs = appendUniqueSnapshotID(activeSnapshotIDs, *repoCtx.snapshotID)
	}
	for _, snapshotID := range repoCtx.activeSnapshotIDs {
		activeSnapshotIDs = appendUniqueSnapshotID(activeSnapshotIDs, snapshotID)
	}
	if err := s.ensureSchema(storage); err != nil {
		return err
	}

	return storage.ResolveIBMiCalls(repoID, repoCtx.snapshotID, activeSnapshotIDs)
}

func queueInsertIBMiExport(batch *pgx.Batch, repoID int64, snapshotID *int64, fileID int64, functionID *int64, objectName, objectType, exportName, sourceType string, lineNumber int) {
	batch.Queue(`
		INSERT INTO ibmi_exports (repo_id, snapshot_id, file_id, function_id, object_name, object_type, export_name, source_type, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
	`, repoID, nullableInt64Ptr(snapshotID), fileID, functionID, nullableString(objectName), nullableString(objectType), exportName, sourceType, lineNumber)
}

func queueInsertIBMiBinding(batch *pgx.Batch, repoID int64, snapshotID *int64, fileID int64, ownerObject, bindingName, bindingType, targetObject, targetObjectType string, lineNumber int) {
	batch.Queue(`
		INSERT INTO ibmi_bindings (repo_id, snapshot_id, file_id, owner_object, binding_name, binding_type, target_object, target_object_type, line_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
	`, repoID, nullableInt64Ptr(snapshotID), fileID, nullableString(ownerObject), bindingName, bindingType, nullableString(targetObject), nullableString(targetObjectType), lineNumber)
}

package graph

import (
	"context"
	"fmt"
	"strings"
)

var ibmiFileLanguages = []string{
	"rpg",
	"rpgle",
	"sqlrpgle",
	"rpgleinc",
	"sqlrpgleinc",
	"cl",
	"clle",
	"clp",
	"dds",
	"dspf",
	"pf",
	"lf",
	"prtf",
	"ibmi_binder",
}

func ibmiLanguageSQL(alias string) string {
	quoted := make([]string, 0, len(ibmiFileLanguages))
	for _, lang := range ibmiFileLanguages {
		quoted = append(quoted, "'"+lang+"'")
	}
	return fmt.Sprintf("%s.language IN (%s)", alias, strings.Join(quoted, ", "))
}

func (storage *Storage) ResolveIBMiCalls(repoID int64, snapshotID *int64, activeSnapshotIDs []int64) error {
	snapshotArg := nullableSnapshotID(snapshotID)
	ctx := context.Background()
	callerIsIBMi := ibmiLanguageSQL("f")
	targetIsIBMi := ibmiLanguageSQL("tf")
	// All stages below require an IBMi caller. Avoid planning the joins against
	// retained history for repositories with no eligible source files.
	var hasIBMi bool
	if err := storage.Executor().QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (
		SELECT 1 FROM files f WHERE f.repo_id=$1
		AND ($2::bigint IS NULL OR f.snapshot_id=$2) AND %s
	)`, callerIsIBMi), repoID, snapshotArg).Scan(&hasIBMi); err != nil {
		return err
	}
	if !hasIBMi {
		return nil
	}

	_, err := storage.Executor().Exec(ctx, fmt.Sprintf(`
		WITH caller AS (
			SELECT fc.id AS fc_id, fn.file_id, fc.callee_name
			FROM function_calls fc
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files f ON f.id = fn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND f.repo_id = $1
			  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
			  AND %s
		),
		unique_file AS (
			SELECT fn.file_id, lower(fn.name) AS name_key
			FROM functions fn
			JOIN files tf ON tf.id = fn.file_id
			WHERE tf.repo_id = $1
			  AND ($2::bigint IS NULL OR tf.snapshot_id = $2)
			  AND %s
			GROUP BY fn.file_id, lower(fn.name)
			HAVING COUNT(*) = 1
		)
		UPDATE function_calls fc
		SET callee_function_id = fn.id,
		    callee_resolution_source = 'ibmi_same_file',
		    callee_resolution_confidence = 'high'
		FROM caller c
		JOIN unique_file u ON u.file_id = c.file_id AND u.name_key = lower(c.callee_name)
		JOIN functions fn ON fn.file_id = c.file_id AND lower(fn.name) = lower(c.callee_name)
		WHERE fc.id = c.fc_id
		  AND fc.callee_function_id IS NULL
	`, callerIsIBMi, targetIsIBMi), repoID, snapshotArg)
	if err != nil {
		return err
	}

	_, err = storage.Executor().Exec(ctx, fmt.Sprintf(`
		WITH caller AS (
			SELECT fc.id AS fc_id, fc.callee_name
			FROM function_calls fc
			JOIN functions fn ON fn.id = fc.caller_function_id
			JOIN files f ON f.id = fn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND f.repo_id = $1
			  AND ($2::bigint IS NULL OR f.snapshot_id = $2)
			  AND %s
		),
		candidates AS (
			SELECT c.fc_id, fn.id AS callee_id
			FROM caller c
			JOIN files tf ON tf.repo_id = $1
			JOIN functions fn ON fn.file_id = tf.id
			WHERE %s
			  AND ($2::bigint IS NULL OR tf.snapshot_id = $2)
			  AND lower(fn.simple_name) = lower(c.callee_name)
		),
		unique_per_call AS (
			SELECT fc_id, MIN(callee_id) AS callee_id
			FROM candidates
			GROUP BY fc_id
			HAVING COUNT(*) = 1
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'ibmi_unique_repo',
		    callee_resolution_confidence = 'medium'
		FROM unique_per_call u
		WHERE fc.id = u.fc_id
		  AND fc.callee_function_id IS NULL
	`, callerIsIBMi, targetIsIBMi), repoID, snapshotArg)
	if err != nil {
		return err
	}

	callerIsIBMi = ibmiLanguageSQL("cf")
	targetIsIBMi = ibmiLanguageSQL("f")

	_, err = storage.Executor().Exec(ctx, fmt.Sprintf(`
		WITH caller AS (
			SELECT fc.id AS fc_id, fc.callee_name
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files cf ON cf.id = cfn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND cf.repo_id = $1
			  AND ($2::bigint[] IS NULL OR cf.snapshot_id = ANY($2))
			  AND %s
			  AND fc.callee_name LIKE '%%.%%'
		),
		unique_global AS (
			SELECT lower(fn.name) AS name_key
			FROM functions fn
			JOIN files f ON f.id = fn.file_id
			WHERE f.repo_id <> $1
			  AND ($2::bigint[] IS NULL OR f.snapshot_id = ANY($2))
			  AND %s
			GROUP BY lower(fn.name)
			HAVING COUNT(*) = 1
		)
		UPDATE function_calls fc
		SET callee_function_id = fn.id,
		    callee_resolution_source = 'qualified_global',
		    callee_resolution_confidence = 'high'
		FROM caller c
		JOIN unique_global u ON u.name_key = lower(c.callee_name)
		JOIN functions fn ON lower(fn.name) = lower(c.callee_name)
		JOIN files f ON f.id = fn.file_id
		WHERE fc.id = c.fc_id
		  AND f.repo_id <> $1
		  AND ($2::bigint[] IS NULL OR f.snapshot_id = ANY($2))
		  AND %s
		  AND fc.callee_function_id IS NULL
	`, callerIsIBMi, targetIsIBMi, targetIsIBMi), repoID, activeSnapshotIDs)
	if err != nil {
		return err
	}

	_, err = storage.Executor().Exec(ctx, fmt.Sprintf(`
		WITH caller AS (
			SELECT fc.id AS fc_id, fc.callee_name
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files cf ON cf.id = cfn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND cf.repo_id = $1
			  AND ($2::bigint[] IS NULL OR cf.snapshot_id = ANY($2))
			  AND %s
			  AND fc.callee_name NOT LIKE '%%.%%'
		),
		candidates AS (
			SELECT c.fc_id, fn.id AS callee_id
			FROM caller c
			JOIN files f ON f.repo_id <> $1
			JOIN functions fn ON fn.file_id = f.id
			WHERE %s
			  AND ($2::bigint[] IS NULL OR f.snapshot_id = ANY($2))
			  AND lower(fn.simple_name) = lower(c.callee_name)
		),
		unique_per_call AS (
			SELECT fc_id, MIN(callee_id) AS callee_id
			FROM candidates
			GROUP BY fc_id
			HAVING COUNT(*) = 1
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'unique_global',
		    callee_resolution_confidence = 'medium'
		FROM unique_per_call u
		WHERE fc.id = u.fc_id
		  AND fc.callee_function_id IS NULL
	`, callerIsIBMi, targetIsIBMi), repoID, activeSnapshotIDs)
	if err != nil {
		return err
	}

	_, err = storage.Executor().Exec(ctx, fmt.Sprintf(`
		WITH caller AS (
			SELECT fc.id AS fc_id, fc.callee_name
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files cf ON cf.id = cfn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND cf.repo_id = $1
			  AND ($2::bigint IS NULL OR cf.snapshot_id = $2)
			  AND %s
		),
		export_candidates AS (
			SELECT DISTINCT
				c.fc_id,
				COALESCE(ie.function_id, fn.id) AS callee_id
			FROM caller c
			JOIN ibmi_exports ie
			  ON ie.repo_id <> $1
			 AND lower(ie.export_name) = lower(c.callee_name)
			 AND ($3::bigint[] IS NULL OR ie.snapshot_id = ANY($3))
			LEFT JOIN files f ON f.id = ie.file_id
			LEFT JOIN functions fn
			  ON ie.function_id IS NULL
			 AND fn.file_id = f.id
			 AND lower(fn.simple_name) = lower(ie.export_name)
			WHERE COALESCE(ie.function_id, fn.id) IS NOT NULL
			  AND (f.id IS NULL OR %s)
		),
		unique_per_call AS (
			SELECT fc_id, MIN(callee_id) AS callee_id
			FROM export_candidates
			GROUP BY fc_id
			HAVING COUNT(*) = 1
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'ibmi_export',
		    callee_resolution_confidence = 'high'
		FROM unique_per_call u
		WHERE fc.id = u.fc_id
		  AND fc.callee_function_id IS NULL
	`, callerIsIBMi, targetIsIBMi), repoID, snapshotArg, activeSnapshotIDs)
	if err != nil {
		return err
	}

	_, err = storage.Executor().Exec(ctx, fmt.Sprintf(`
		WITH caller AS (
			SELECT fc.id AS fc_id,
			       fc.callee_name,
			       cf.id AS caller_file_id
			FROM function_calls fc
			JOIN functions cfn ON cfn.id = fc.caller_function_id
			JOIN files cf ON cf.id = cfn.file_id
			WHERE fc.callee_function_id IS NULL
			  AND cf.repo_id = $1
			  AND ($2::bigint IS NULL OR cf.snapshot_id = $2)
			  AND %s
		),
		caller_bindings AS (
			SELECT c.fc_id, c.callee_name, b.binding_name
			FROM caller c
			JOIN ibmi_bindings b
			  ON b.file_id = c.caller_file_id
			 AND b.binding_type = 'bnddir_ref'
			 AND ($2::bigint IS NULL OR b.snapshot_id = $2)
		),
		candidates AS (
			SELECT DISTINCT
				cb.fc_id,
				COALESCE(ie.function_id, fn.id) AS callee_id
			FROM caller_bindings cb
			JOIN ibmi_bindings dir
			  ON dir.repo_id <> $1
			 AND dir.binding_type = 'bnddir_entry'
			 AND lower(dir.binding_name) = lower(cb.binding_name)
			 AND ($3::bigint[] IS NULL OR dir.snapshot_id = ANY($3))
			JOIN ibmi_exports ie
			  ON ie.repo_id = dir.repo_id
			 AND lower(ie.export_name) = lower(cb.callee_name)
			 AND ($3::bigint[] IS NULL OR ie.snapshot_id = ANY($3))
			 AND (
			 	dir.target_object IS NULL
			 	OR ie.object_name IS NULL
			 	OR lower(ie.object_name) = lower(dir.target_object)
			 )
			LEFT JOIN files f ON f.id = ie.file_id
			LEFT JOIN functions fn
			  ON ie.function_id IS NULL
			 AND fn.file_id = f.id
			 AND lower(fn.simple_name) = lower(ie.export_name)
			WHERE COALESCE(ie.function_id, fn.id) IS NOT NULL
			  AND (f.id IS NULL OR %s)
		),
		unique_per_call AS (
			SELECT fc_id, MIN(callee_id) AS callee_id
			FROM candidates
			GROUP BY fc_id
			HAVING COUNT(*) = 1
		)
		UPDATE function_calls fc
		SET callee_function_id = u.callee_id,
		    callee_resolution_source = 'ibmi_binding',
		    callee_resolution_confidence = 'high'
		FROM unique_per_call u
		WHERE fc.id = u.fc_id
		  AND fc.callee_function_id IS NULL
	`, callerIsIBMi, targetIsIBMi), repoID, snapshotArg, activeSnapshotIDs)
	if err != nil {
		return err
	}

	return nil
}

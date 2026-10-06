package graph

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type classLocation struct {
	name       string
	start, end int
}

// ClassDeclarations binds enrichment to the declarations persisted from the
// indexed source. A name alone is not an identity, even within one file.
type ClassDeclarations struct {
	IDs    []int64
	bySpan map[classLocation][]int64
}

func LoadClassDeclarations(ctx context.Context, tx pgx.Tx, fileID int64) (*ClassDeclarations, error) {
	rows, err := tx.Query(ctx, `SELECT id,name,start_line,end_line FROM classes WHERE file_id=$1`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	declarations := &ClassDeclarations{bySpan: make(map[classLocation][]int64)}
	for rows.Next() {
		var id int64
		var location classLocation
		if err := rows.Scan(&id, &location.name, &location.start, &location.end); err != nil {
			return nil, err
		}
		declarations.IDs = append(declarations.IDs, id)
		declarations.bySpan[location] = append(declarations.bySpan[location], id)
	}
	return declarations, rows.Err()
}

func (d *ClassDeclarations) Match(name string, startLine, endLine int) (int64, error) {
	ids := d.bySpan[classLocation{name, startLine, endLine}]
	if len(ids) != 1 {
		return 0, fmt.Errorf("indexed class %q at lines %d-%d has %d matching declarations; run a full reindex with the current parser and helpers", name, startLine, endLine, len(ids))
	}
	return ids[0], nil
}

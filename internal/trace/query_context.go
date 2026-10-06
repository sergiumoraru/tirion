package trace

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Queryer is the read-only database surface used by traversal. A context-bound
// queryer carries the same request deadline through all recursive lookups.
type Queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type contextualQueries struct {
	Queryer
	ctx context.Context
}

// Bound queries always use the owning request, even when a legacy helper supplies
// Background. This prevents post-traversal summary lookups from outliving it.
func (q *contextualQueries) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.Queryer.Query(q.ctx, sql, args...)
}
func (q *contextualQueries) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return q.Queryer.QueryRow(q.ctx, sql, args...)
}

func WithContext(ctx context.Context, pool Queryer) Queryer {
	return &contextualQueries{Queryer: pool, ctx: ctx}
}
func queryContext(pool Queryer) context.Context {
	if bound, ok := pool.(*contextualQueries); ok {
		return bound.ctx
	}
	return context.Background()
}

const MaxDepth = 32
const MaxNodes = 10000

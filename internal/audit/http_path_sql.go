package audit

import "fmt"

func normalizedHTTPCallPathExpr(alias string) string {
	pathExpr := fmt.Sprintf("lower(split_part(split_part(coalesce(%s.url_pattern, ''), '?', 1), '#', 1))", alias)
	pathExpr = fmt.Sprintf("regexp_replace(%s, '^https?://[^/]+', '')", pathExpr)
	pathExpr = fmt.Sprintf("regexp_replace(%s, '^(/(?::[^/]+|\\{[^/]+\\}))+(?=/)', '')", pathExpr)
	pathExpr = fmt.Sprintf("regexp_replace(%s, '/{2,}', '/', 'g')", pathExpr)
	pathExpr = fmt.Sprintf("CASE WHEN %s = '/' THEN '/' ELSE regexp_replace(%s, '/+$', '') END", pathExpr, pathExpr)
	return fmt.Sprintf("COALESCE(NULLIF(%s, ''), '/')", pathExpr)
}

func routeLikeHTTPCallExpr(methodExpr, pathExpr string) string {
	return fmt.Sprintf(`(
		%s ~ '^/(?:[a-z0-9._-]+|:[a-z0-9._-]+|\{[a-z0-9._-]+\})(?:/(?:[a-z0-9._-]+|:[a-z0-9._-]+|\{[a-z0-9._-]+\}))*$'
		AND %s <> '/'
		AND NOT (%s = 'REQUEST' AND %s ~ '^/api[^/]*$')
		AND NOT (%s = 'REQUEST' AND %s ~ '^/:[^/]+$')
	)`, pathExpr, pathExpr, methodExpr, pathExpr, methodExpr, pathExpr)
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sergiumoraru/tirion/internal/parser"
)

type myBatisStatementCall struct {
	relPath      string
	functionName string
	statementID  string
	lineNumber   int
}

type myBatisFileEntry struct {
	relPath  string
	accesses []myBatisXMLAccess
}

type myBatisXMLModule struct {
	java          *javaParserModule
	pendingFiles  []myBatisFileEntry
	pendingCalls  []myBatisStatementCall
	accessesByRel map[string][]myBatisXMLAccess
}

func newMyBatisXMLModule(java *javaParserModule) *myBatisXMLModule {
	return &myBatisXMLModule{
		java:          java,
		accessesByRel: make(map[string][]myBatisXMLAccess),
	}
}

func (m *myBatisXMLModule) ID() string {
	return "mybatis_xml"
}

func (m *myBatisXMLModule) CanParse(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".xml")
}

func (m *myBatisXMLModule) SkipContent(relPath, absPath string, content []byte) (parseSkipCategory, bool) {
	if !maybeMyBatisXMLPath(absPath) {
		return parseSkipXMLNotMapper, true
	}
	if !likelyMyBatisMapperContent(content) {
		return parseSkipXMLNotMapper, true
	}
	accesses := extractMyBatisXMLAccesses(content)
	if len(accesses) == 0 {
		delete(m.accessesByRel, relPath)
		return parseSkipNoExtractedContent, true
	}
	m.accessesByRel[relPath] = accesses
	return "", false
}

func (m *myBatisXMLModule) Parse(relPath, absPath string, content []byte) parser.ParsedFile {
	accesses := m.accessesByRel[relPath]
	if len(accesses) == 0 {
		accesses = extractMyBatisXMLAccesses(content)
	}
	if len(accesses) > 0 {
		m.pendingFiles = append(m.pendingFiles, myBatisFileEntry{
			relPath:  relPath,
			accesses: accesses,
		})
	}
	return parser.ParsedFile{
		Path:     relPath,
		Language: "xml",
	}
}

func (m *myBatisXMLModule) FinalizeRepo(ctx *repoFinalizeContext) error {
	if ctx == nil || ctx.storage == nil {
		return nil
	}
	if len(m.pendingFiles) == 0 && len(m.pendingCalls) == 0 {
		return nil
	}

	tx, err := ctx.storage.Pool().Begin(context.Background())
	if err != nil {
		return err
	}

	dbBatch := &pgx.Batch{}
	seenAccess := make(map[string]bool)
	statementIndex := make(map[string][]sqlAccess)
	statementIndexUnqualified := make(map[string][]sqlAccess)
	appendUnique := func(dst map[string][]sqlAccess, key string, value sqlAccess) {
		list := dst[key]
		for _, existing := range list {
			if strings.EqualFold(existing.Entity, value.Entity) && existing.Access == value.Access {
				return
			}
		}
		dst[key] = append(dst[key], value)
	}

	for _, entry := range m.pendingFiles {
		for _, access := range entry.accesses {
			if access.Namespace != "" && access.Method != "" {
				qualifiedID := access.Namespace + "." + access.Method
				appendUnique(statementIndex, qualifiedID, sqlAccess{Entity: access.Entity, Access: access.Access})
				appendUnique(statementIndexUnqualified, access.Method, sqlAccess{Entity: access.Entity, Access: access.Access})
			}

			callerFile := resolveNamespacePathFromIndex(access.Namespace, m.java.pathIndex)
			if callerFile == "" {
				continue
			}
			callerName := simpleNameFromNamespace(access.Namespace) + "." + access.Method
			if callerName == "." {
				continue
			}
			callerID := fmt.Sprintf("%s:%s:%s", ctx.repoName, callerFile, callerName)
			key := callerID + "|" + strings.ToLower(access.Entity) + "|" + access.Access
			if seenAccess[key] {
				continue
			}
			seenAccess[key] = true
			queueInsertDataAccess(dbBatch, ctx.repoID, ctx.snapshotID, callerID, access.Entity, access.Access, access.Line)
		}
	}

	if len(m.pendingCalls) > 0 && (len(statementIndex) > 0 || len(statementIndexUnqualified) > 0) {
		for _, call := range m.pendingCalls {
			var accesses []sqlAccess
			if strings.Contains(call.statementID, ".") {
				accesses = statementIndex[call.statementID]
			} else {
				accesses = statementIndexUnqualified[call.statementID]
				if len(accesses) != 1 {
					continue
				}
			}
			if len(accesses) == 0 {
				continue
			}
			callerID := fmt.Sprintf("%s:%s:%s", ctx.repoName, call.relPath, call.functionName)
			for _, access := range accesses {
				key := callerID + "|" + strings.ToLower(access.Entity) + "|" + access.Access
				if seenAccess[key] {
					continue
				}
				seenAccess[key] = true
				queueInsertDataAccess(dbBatch, ctx.repoID, ctx.snapshotID, callerID, access.Entity, access.Access, call.lineNumber)
			}
		}
	}

	if dbBatch.Len() > 0 {
		results := tx.SendBatch(context.Background(), dbBatch)
		if err := results.Close(); err != nil {
			_ = tx.Rollback(context.Background())
			return err
		}
	}
	if err := tx.Commit(context.Background()); err != nil {
		return err
	}

	m.pendingFiles = nil
	m.pendingCalls = nil
	m.accessesByRel = make(map[string][]myBatisXMLAccess)
	return nil
}

func (m *myBatisXMLModule) RecordSQLStatements(relPath string, statements map[string][]parser.ParsedSqlStatementCall) {
	if len(statements) == 0 {
		return
	}
	for fnName, calls := range statements {
		if fnName == "" {
			continue
		}
		for _, call := range calls {
			if call.StatementID == "" || call.Access == "" {
				continue
			}
			m.pendingCalls = append(m.pendingCalls, myBatisStatementCall{
				relPath:      relPath,
				functionName: fnName,
				statementID:  call.StatementID,
				lineNumber:   call.LineNumber,
			})
		}
	}
}

func likelyMyBatisMapperContent(content []byte) bool {
	head := bytes.ToLower(content)
	return bytes.Contains(head, []byte("<mapper")) || bytes.Contains(head, []byte("<sqlmap"))
}

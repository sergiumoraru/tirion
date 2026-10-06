package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sergiumoraru/tirion/internal/parser"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
)

// Project dependency metadata binds generated classes to an EDMX model. Folder
// names and uppercase class names are not evidence of a database mapping.
var errExcludedCSharpLink = errors.New("excluded project source symlink")

type csharpEntityModule struct {
	root     string
	parser   *parser.CSharpParser
	parsed   map[string][]parser.ParsedJpaEntity
	projects map[string]map[string][]parser.ParsedJpaEntity
}

func newCSharpEntityModule(root string) *csharpEntityModule {
	abs, _ := filepath.Abs(root)
	return &csharpEntityModule{root: abs, parser: parser.NewCSharpParser(),
		parsed: make(map[string][]parser.ParsedJpaEntity), projects: make(map[string]map[string][]parser.ParsedJpaEntity)}
}

func (m *csharpEntityModule) ID() string                { return "csharp" }
func (m *csharpEntityModule) CanParse(path string) bool { return m.parser.CanParse(path) }
func (m *csharpEntityModule) Parse(relPath, absPath string, content []byte) parser.ParsedFile {
	result := m.parser.ParseFile(relPath, content)
	m.parsed[relPath] = result.JpaEntities
	return result
}

func (m *csharpEntityModule) FinalizeRepo(scope *repoFinalizeContext) error {
	ctx := context.Background()
	rows, err := scope.storage.Pool().Query(ctx, `SELECT c.id, c.name, f.path, COALESCE(f.hash,''), rs.sha
		FROM classes c JOIN files f ON f.id=c.file_id JOIN repo_snapshots rs ON rs.id=f.snapshot_id
		WHERE f.repo_id=$1 AND f.snapshot_id IS NOT DISTINCT FROM $2::bigint
		AND f.language='csharp' ORDER BY f.path, c.id`, scope.repoID, nullableInt64Ptr(scope.snapshotID))
	if err != nil {
		return err
	}
	type class struct {
		id                         int64
		name, path, hash, revision string
	}
	var classes []class
	for rows.Next() {
		var c class
		if err := rows.Scan(&c.id, &c.name, &c.path, &c.hash, &c.revision); err != nil {
			rows.Close()
			return err
		}
		classes = append(classes, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(classes) == 0 {
		return nil
	}

	mappings := make(map[int64]parser.ParsedJpaEntity)
	ids := make([]int64, 0, len(classes))
	for _, c := range classes {
		ids = append(ids, c.id)
		file, err := m.localPath(m.root, c.path)
		if os.IsNotExist(err) || errors.Is(err, errExcludedCSharpLink) {
			continue // Incremental stale-file cleanup runs after finalizers.
		}
		if err != nil {
			return err
		}
		explicit, ok := m.parsed[c.path]
		if !ok {
			content, err := sourceindex.Read(ctx, m.root, c.path, c.hash, c.revision)
			if err != nil {
				return err
			}
			content, _ = sourceindex.NormalizeText(content)
			result := m.Parse(c.path, file, content)
			// The main pass records a failed file and continues; a recovered partial
			// tree keeps its entities. Aborting the repository here would make a file
			// the parse step tolerated fatal only on incremental runs.
			if result.ParseDiagnostics.Failed() {
				fmt.Fprintf(os.Stderr, "Warning: C# entity metadata %s: %s: %s\n", c.path, result.ParseDiagnostics.FailureKind, result.ParseDiagnostics.Message)
			}
			explicit = result.JpaEntities
		}
		for _, entity := range explicit {
			if entity.ClassName == c.name {
				mappings[c.id] = entity
			}
		}
		if _, exists := mappings[c.id]; exists {
			continue
		}
		candidates := make(map[parser.ParsedJpaEntity]bool)
		for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
			projects, err := filepath.Glob(filepath.Join(dir, "*.csproj"))
			if err != nil {
				return err
			}
			for _, project := range projects {
				bindings, ok := m.projects[project]
				if !ok {
					bindings, err = m.loadProject(project)
					if errors.Is(err, errExcludedCSharpLink) {
						m.projects[project] = nil
						continue
					}
					if err != nil {
						return err
					}
					m.projects[project] = bindings
				}
				for _, entity := range bindings[file] {
					if entity.ClassName == c.name {
						candidates[entity] = true
					}
				}
			}
			if dir == m.root {
				break
			}
		}
		// The existing entity table represents one table per class. Do not pick
		// an arbitrary fragment for entity splitting or conflicting projects.
		if len(candidates) == 1 {
			for entity := range candidates {
				mappings[c.id] = entity
			}
		}
	}
	tx, err := scope.storage.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Recompute even for unchanged .cs files when project/model metadata changes.
	if _, err := tx.Exec(ctx, `DELETE FROM jpa_entities WHERE class_id=ANY($1::bigint[])`, ids); err != nil {
		return err
	}
	for id, e := range mappings {
		if _, err := tx.Exec(ctx, `INSERT INTO jpa_entities(class_id,table_name,schema_name,catalog)
			VALUES($1,$2,NULLIF($3,''),NULLIF($4,''))`, id, e.TableName, e.Schema, e.Catalog); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (m *csharpEntityModule) localPath(base, path string) (string, error) {
	path = strings.ReplaceAll(path, `\`, `/`)
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute project dependency: %s", path)
	}
	full := filepath.Clean(filepath.Join(base, filepath.FromSlash(path)))
	rel, err := filepath.Rel(m.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("project dependency outside repository: %s", path)
	}
	excluded, err := sourceindex.ExcludedLink(m.root, rel)
	if err != nil {
		return "", err
	}
	if excluded {
		fmt.Fprintf(os.Stderr, "Skipping non-source project symlink: %s\n", rel)
		return "", errExcludedCSharpLink
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(m.root)
	if err != nil {
		return "", err
	}
	rel, err = filepath.Rel(realRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("project dependency symlink outside repository: %s", path)
	}
	return full, nil
}

type entityProjectItem struct {
	XMLName       xml.Name
	Include       string `xml:"Include,attr"`
	Condition     string `xml:"Condition,attr"`
	DependentUpon string `xml:"DependentUpon"`
}

func (m *csharpEntityModule) loadProject(project string) (map[string][]parser.ParsedJpaEntity, error) {
	project, err := m.localPath(filepath.Dir(project), filepath.Base(project))
	if err != nil {
		return nil, err
	}
	var doc struct {
		XMLName xml.Name
		Groups  []struct {
			Condition string              `xml:"Condition,attr"`
			Items     []entityProjectItem `xml:",any"`
		} `xml:"ItemGroup"`
	}
	content, err := m.readFile(project)
	if err != nil {
		return nil, err
	}
	if err := xml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("project XML %s: %w", project, err)
	}
	if doc.XMLName.Local != "Project" {
		return nil, fmt.Errorf("project XML %s has root %s", project, doc.XMLName.Local)
	}
	dependencies := make(map[string]string)
	conflicting := make(map[string]bool)
	var files []string
	for _, group := range doc.Groups {
		if group.Condition != "" {
			continue
		}
		for _, item := range group.Items {
			if item.Condition != "" || item.DependentUpon == "" || strings.ContainsAny(item.Include+item.DependentUpon, "$*?") {
				continue
			}
			// Only model/template dependencies are relevant. Other project items
			// may reference optional generated files that are not checked in.
			ext := strings.ToLower(filepath.Ext(item.DependentUpon))
			if ext != ".tt" && ext != ".edmx" {
				continue
			}
			file, err := m.localPath(filepath.Dir(project), item.Include)
			if errors.Is(err, errExcludedCSharpLink) {
				continue
			}
			if err != nil {
				return nil, err
			}
			parent, err := m.localPath(filepath.Dir(file), item.DependentUpon)
			if errors.Is(err, errExcludedCSharpLink) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if previous, exists := dependencies[file]; exists && previous != parent {
				conflicting[file] = true
			}
			dependencies[file] = parent
			if item.XMLName.Local == "Compile" {
				files = append(files, file)
			}
		}
	}
	models := make(map[string][]parser.ParsedJpaEntity)
	out := make(map[string][]parser.ParsedJpaEntity)
	for _, file := range files {
		if conflicting[file] {
			continue
		}
		parent := dependencies[file]
		seen := make(map[string]bool)
		for parent != "" && !seen[parent] && !strings.EqualFold(filepath.Ext(parent), ".edmx") {
			if conflicting[parent] {
				parent = ""
				break
			}
			seen[parent] = true
			parent = dependencies[parent]
		}
		if !strings.EqualFold(filepath.Ext(parent), ".edmx") {
			continue
		}
		entities, ok := models[parent]
		if !ok {
			content, err := m.readFile(parent)
			if err != nil {
				return nil, err
			}
			entities, err = parseEDMXEntities(content)
			if err != nil {
				return nil, fmt.Errorf("EDMX %s: %w", parent, err)
			}
			models[parent] = entities
		}
		out[file] = entities
	}
	return out, nil
}

func parseEDMXEntities(content []byte) ([]parser.ParsedJpaEntity, error) {
	var doc struct {
		XMLName xml.Name
		Stores  []struct {
			Name   string `xml:"Name,attr"`
			Schema string `xml:"Schema,attr"`
			Table  string `xml:"Table,attr"`
			Query  string `xml:"DefiningQuery"`
		} `xml:"Runtime>StorageModels>Schema>EntityContainer>EntitySet"`
		Mappings []struct {
			Type      string `xml:"TypeName,attr"`
			Fragments []struct {
				Store string `xml:"StoreEntitySet,attr"`
			} `xml:"MappingFragment"`
		} `xml:"Runtime>Mappings>Mapping>EntityContainerMapping>EntitySetMapping>EntityTypeMapping"`
	}
	if err := xml.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	if doc.XMLName.Local != "Edmx" {
		return nil, fmt.Errorf("expected Edmx root, got %s", doc.XMLName.Local)
	}
	stores := make(map[string]parser.ParsedJpaEntity)
	storeCounts := make(map[string]int)
	for _, s := range doc.Stores {
		storeCounts[s.Name]++
	}
	for _, s := range doc.Stores {
		if storeCounts[s.Name] != 1 {
			continue
		}
		if s.Query != "" {
			// Database-first keyless sets use a defining SELECT instead of a
			// Table attribute. Reuse SQL extraction, retaining only an unambiguous
			// single-relation mapping; a joined query is not one table.
			facts := parser.NewSQLParser().ParseFile("model.sql", []byte(s.Query))
			relations := make(map[string]bool)
			readOnly := true
			for _, accesses := range facts.DataAccesses {
				for _, access := range accesses {
					readOnly = readOnly && access.Access == "read"
					relations[access.EntityName] = true
				}
			}
			if readOnly && len(relations) == 1 {
				for relation := range relations {
					parts := strings.Split(relation, ".")
					entity := parser.ParsedJpaEntity{TableName: parts[len(parts)-1], Schema: s.Schema}
					if len(parts) > 1 {
						entity.Schema = parts[len(parts)-2]
					}
					if len(parts) == 3 {
						entity.Catalog = parts[0]
					}
					if len(parts) <= 3 {
						stores[s.Name] = entity
					}
				}
			}
			continue
		}
		table := s.Table
		if table == "" {
			table = s.Name
		}
		stores[s.Name] = parser.ParsedJpaEntity{TableName: table, Schema: s.Schema}
	}
	var out []parser.ParsedJpaEntity
	for _, mapping := range doc.Mappings {
		typeName := strings.TrimSuffix(strings.TrimPrefix(mapping.Type, "IsTypeOf("), ")")
		if i := strings.LastIndex(typeName, "."); i >= 0 {
			typeName = typeName[i+1:]
		}
		if typeName == "" {
			continue
		}
		for _, fragment := range mapping.Fragments {
			if e, ok := stores[fragment.Store]; ok {
				e.ClassName = typeName
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func (m *csharpEntityModule) readFile(path string) ([]byte, error) {
	rel, err := filepath.Rel(m.root, path)
	if err != nil {
		return nil, err
	}
	return sourceindex.ReadCurrent(m.root, rel)
}

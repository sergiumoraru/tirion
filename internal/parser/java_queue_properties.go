package parser

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sergiumoraru/tirion/internal/config"
	"github.com/sergiumoraru/tirion/internal/sourceindex"
	"github.com/sergiumoraru/tirion/internal/sourcepath"
)

func LoadJavaQueueProperties(root string) (map[string]string, error) {
	if root == "" {
		return map[string]string{}, nil
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && sourcepath.SkipDir(filepath.Dir(path), d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".properties") {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			// Dangling, escaping and non-regular links are not repository-owned
			// source; metadata capture excludes them identically.
			excluded, err := sourceindex.ExcludedLink(root, rel)
			if err != nil {
				return err
			}
			if !excluded {
				paths = append(paths, rel)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return LoadJavaQueuePropertiesFrom(paths, func(path string) ([]byte, error) { return sourceindex.ReadCurrent(root, path) })
}

// LoadJavaQueuePropertiesFrom parses a captured metadata list; the supplied reader
// must enforce the snapshot's content hashes when used by enrichment.
func LoadJavaQueuePropertiesFrom(paths []string, read func(string) ([]byte, error)) (map[string]string, error) {
	props := make(map[string]string)
	conflicting := make(map[string]bool)
	sort.Strings(paths)
	for _, path := range paths {
		if !strings.HasSuffix(strings.ToLower(path), ".properties") {
			continue
		}
		// Callers pass paths captured with sourcepath.SkipDir already applied;
		// re-check only the manifest-independent names, which need no filesystem.
		skip := false
		parts := strings.Split(filepath.ToSlash(path), "/")
		for _, part := range parts[:len(parts)-1] {
			if sourcepath.AlwaysSkippedDir(part) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		data, err := read(path)
		if err != nil {
			return nil, err
		}
		if len(data) > 2*1024*1024 {
			return nil, fmt.Errorf("queue properties file exceeds supported size: %s", path)
		}

		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), 2*1024*1024)
		var currentKey string
		var currentValue strings.Builder
		setProp := func(key, value string) {
			if key == "" || conflicting[key] {
				return
			}
			existing, ok := props[key]
			if ok && existing != value {
				delete(props, key)
				conflicting[key] = true
				return
			}
			props[key] = value
		}
		flush := func() {
			if currentKey == "" {
				return
			}
			value := strings.TrimSpace(currentValue.String())
			value = strings.Trim(value, `"'`)
			setProp(currentKey, value)
			currentKey = ""
			currentValue.Reset()
		}

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
				continue
			}
			// handle continuation from previous line
			if currentKey != "" {
				if strings.HasSuffix(line, "\\") {
					currentValue.WriteString(strings.TrimSuffix(line, "\\"))
					continue
				}
				currentValue.WriteString(line)
				flush()
				continue
			}

			sepIdx := strings.IndexAny(line, "=:")
			if sepIdx < 0 {
				continue
			}
			key := strings.TrimSpace(line[:sepIdx])
			if key == "" {
				continue
			}
			value := strings.TrimSpace(line[sepIdx+1:])
			if strings.HasSuffix(value, "\\") {
				currentKey = key
				currentValue.WriteString(strings.TrimSuffix(value, "\\"))
				continue
			}
			value = strings.Trim(value, `"'`)
			setProp(key, value)
		}
		flush()
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	return props, nil
}

func classHasMethod(cls ParsedClass, functions []ParsedFunction, method string) bool {
	for _, fn := range cls.Methods {
		if fn.Name == method || strings.HasSuffix(fn.Name, "."+method) {
			return true
		}
	}
	// Java methods are stored in the file's qualified function list, not copied
	// into ParsedClass.Methods. Keep the range check for same-named nested types.
	for _, fn := range functions {
		if fn.Name == cls.Name+"."+method && fn.StartLine >= cls.StartLine && fn.EndLine <= cls.EndLine {
			return true
		}
	}
	return false
}

func DerivePropertyQueueConsumers(result ParsedFile, propertyMap map[string]string, framework config.JavaSQSFramework) []ParsedSqsConsumer {
	if len(propertyMap) == 0 || framework.PropertyAnnotation == "" {
		return nil
	}
	consumers := make([]ParsedSqsConsumer, 0)
	seen := make(map[string]bool)

	for _, cls := range result.Classes {
		if !framework.MatchesConsumerType(append([]string{cls.ExtendsClass}, cls.Implements...)) {
			continue
		}
		handlerMethod := framework.HandlerMethod
		if !classHasMethod(cls, result.Functions, handlerMethod) {
			continue
		}

		for _, ctor := range cls.Constructors {
			for _, param := range ctor.Parameters {
				if param.Annotation != framework.PropertyAnnotation {
					continue
				}
				key := strings.TrimSpace(strings.Trim(param.AnnotationValue, `"'`))
				if key == "" {
					continue
				}
				queueName := propertyMap[key]
				if queueName == "" {
					continue
				}
				dedupeKey := cls.Name + "|" + handlerMethod + "|" + queueName
				if seen[dedupeKey] {
					continue
				}
				seen[dedupeKey] = true
				consumers = append(consumers, ParsedSqsConsumer{
					QueueName:     queueName,
					HandlerMethod: handlerMethod,
					ClassName:     cls.Name,
				})
			}
		}
	}

	return consumers
}

func HasPropertyQueueConsumer(result ParsedFile, framework config.JavaSQSFramework) bool {
	if framework.PropertyAnnotation == "" {
		return false
	}
	for _, cls := range result.Classes {
		if !framework.MatchesConsumerType(append([]string{cls.ExtendsClass}, cls.Implements...)) {
			continue
		}
		for _, ctor := range cls.Constructors {
			for _, param := range ctor.Parameters {
				if param.Annotation != framework.PropertyAnnotation {
					continue
				}
				if strings.TrimSpace(strings.Trim(param.AnnotationValue, `"'`)) != "" {
					return true
				}
			}
		}
	}
	return false
}

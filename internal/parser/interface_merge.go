package parser

import (
	"encoding/json"
	"slices"
)

// MergeInterfaceDeclarations is shared by native persistence and enrichment.
// Preserve overloads and all declaration members under the file/name identity.
func MergeInterfaceDeclarations(in []ParsedInterface) []ParsedInterface {
	out := make([]ParsedInterface, 0, len(in))
	positions := make(map[string]int, len(in))
	for _, next := range in {
		position, exists := positions[next.Name]
		if !exists {
			position = len(out)
			positions[next.Name] = position
			out = append(out, ParsedInterface{Name: next.Name, StartLine: next.StartLine, EndLine: next.EndLine})
		}
		merged := &out[position]
		merged.StartLine = min(merged.StartLine, next.StartLine)
		merged.EndLine = max(merged.EndLine, next.EndLine)
		merged.IsExported = merged.IsExported || next.IsExported
		merged.IsFunctional = merged.IsFunctional || next.IsFunctional
		for _, parent := range next.ExtendsInterfaces {
			if !slices.Contains(merged.ExtendsInterfaces, parent) {
				merged.ExtendsInterfaces = append(merged.ExtendsInterfaces, parent)
			}
		}
		merged.Methods = mergeInterfaceMembers(merged.Methods, next.Methods, func(fn ParsedFunction) ParsedFunction {
			fn.StartLine, fn.EndLine, fn.SourceCode = 0, 0, ""
			return fn
		})
		merged.Fields = mergeInterfaceMembers(merged.Fields, next.Fields, func(field ParsedField) ParsedField {
			field.StartLine = 0
			return field
		})
		merged.TypeParameters = mergeInterfaceMembers(merged.TypeParameters, next.TypeParameters, func(param ParsedTypeParameter) ParsedTypeParameter { return param })
		merged.Annotations = mergeInterfaceMembers(merged.Annotations, next.Annotations, func(annotation ParsedAnnotation) ParsedAnnotation {
			annotation.LineNumber = 0
			return annotation
		})
	}
	return out
}

func mergeInterfaceMembers[T any](existing, incoming []T, signature func(T) T) []T {
	out := slices.Clone(existing)
	seen := make(map[string]bool, len(existing)+len(incoming))
	for _, member := range existing {
		encoded, err := json.Marshal(signature(member))
		if err == nil {
			seen[string(encoded)] = true
		}
	}
	for _, member := range incoming {
		encoded, err := json.Marshal(signature(member))
		if err == nil && seen[string(encoded)] {
			continue
		}
		if err == nil {
			seen[string(encoded)] = true
		}
		out = append(out, member)
	}
	return out
}

package comparisoninternal

import (
	"reflect"
	"slices"
	"sort"
)

// maxDifferences bounds both result size and work after divergence is established.
const maxDifferences = 100

// diffValues compares the structure left after successful custom comparisons are pruned.
func diffValues(reference, candidate any, path documentPath, differences []string) []string {
	if len(differences) >= maxDifferences || reflect.DeepEqual(reference, candidate) {
		return differences
	}
	switch referenceValue := reference.(type) {
	case map[string]any:
		candidateValue, ok := candidate.(map[string]any)
		if !ok {
			return appendDifference(differences, path.String())
		}
		keys := map[string]struct{}{}
		for key := range referenceValue {
			keys[key] = struct{}{}
		}
		for key := range candidateValue {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			referenceItem, referencePresent := referenceValue[key]
			candidateItem, candidatePresent := candidateValue[key]
			itemPath := path.appendField(key)
			if referencePresent != candidatePresent {
				differences = appendDifference(differences, itemPath.String())
				continue
			}
			differences = diffValues(referenceItem, candidateItem, itemPath, differences)
		}
		return differences
	case []any:
		candidateValue, ok := candidate.([]any)
		if !ok {
			return appendDifference(differences, path.String())
		}
		length := max(len(referenceValue), len(candidateValue))
		for index := range length {
			itemPath := path.appendIndex(index)
			if index >= len(referenceValue) || index >= len(candidateValue) {
				differences = appendDifference(differences, itemPath.String())
				continue
			}
			differences = diffValues(referenceValue[index], candidateValue[index], itemPath, differences)
		}
		return differences
	default:
		return appendDifference(differences, path.String())
	}
}

func appendDifference(differences []string, path string) []string {
	if len(differences) >= maxDifferences {
		return differences
	}
	if slices.Contains(differences, path) {
		return differences
	}
	return append(differences, path)
}

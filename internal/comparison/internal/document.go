package comparisoninternal

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecthomas/errors"
)

// document is one mutable comparison copy; deleting from it never mutates captured data.
type document struct {
	root    any
	present bool
}

func newDocument(data []byte) (*document, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, errors.Wrap(err, "decode comparison JSON")
	}
	return &document{root: root, present: true}, nil
}

func (d *document) Value(path documentPath) documentValue {
	return path.value(d.root, d.present)
}

func (d *document) Delete(path documentPath) error {
	if !d.present {
		return nil
	}
	if path.empty() {
		d.root = nil
		d.present = false
		return nil
	}
	updated, err := path.delete(d.root)
	if err != nil {
		return err
	}
	d.root = updated
	return nil
}

func (d *document) Export() any {
	if !d.present {
		return nil
	}
	return d.root
}

// documentValue distinguishes an absent path from a present JSON null value.
type documentValue struct {
	value   any
	present bool
}

func newDocumentValue(value any, present bool) documentValue {
	return documentValue{value: value, present: present}
}

func (v documentValue) isPresent() bool {
	return v.present
}

func (v documentValue) comparatorArgument() (value any, present bool) {
	return v.value, v.present
}

func (v documentValue) array() ([]any, bool) {
	if !v.present || v.value == nil {
		return nil, false
	}
	array, ok := v.value.([]any)
	return array, ok
}

func (v documentValue) object() (map[string]any, bool) {
	if !v.present || v.value == nil {
		return nil, false
	}
	object, ok := v.value.(map[string]any)
	return object, ok
}

// documentPath is immutable so precomputed occurrences remain valid while documents mutate.
type documentPath struct {
	parts []pathPart
}

func newDocumentPath(parts []pathPart) documentPath {
	return documentPath{parts: parts}
}

func (p documentPath) appendField(name string) documentPath {
	return p.append(newFieldPathPart(name))
}

func (p documentPath) appendIndex(index int) documentPath {
	return p.append(newIndexPathPart(index))
}

func (p documentPath) append(part pathPart) documentPath {
	parts := make([]pathPart, len(p.parts), len(p.parts)+1)
	copy(parts, p.parts)
	return newDocumentPath(append(parts, part))
}

func (p documentPath) empty() bool {
	return len(p.parts) == 0
}

func (p documentPath) depth() int {
	return len(p.parts)
}

func (p documentPath) indexedSiblingBefore(other documentPath) bool {
	// Descending array indexes prevent an earlier deletion from shifting a later path.
	if len(p.parts) == 0 || len(p.parts) != len(other.parts) {
		return false
	}
	leftIndex, leftIndexed := p.parts[len(p.parts)-1].arrayIndex()
	rightIndex, rightIndexed := other.parts[len(other.parts)-1].arrayIndex()
	if !leftIndexed || !rightIndexed || !equalPathParts(p.parts[:len(p.parts)-1], other.parts[:len(other.parts)-1]) {
		return false
	}
	return leftIndex > rightIndex
}

func (p documentPath) value(root any, present bool) documentValue {
	if !present {
		return newDocumentValue(nil, false)
	}
	value := root
	for _, part := range p.parts {
		if index, indexed := part.arrayIndex(); indexed {
			array, ok := value.([]any)
			if !ok || index < 0 || index >= len(array) {
				return newDocumentValue(nil, false)
			}
			value = array[index]
			continue
		}
		name, named := part.fieldName()
		if !named {
			return newDocumentValue(nil, false)
		}
		object, ok := value.(map[string]any)
		if !ok {
			return newDocumentValue(nil, false)
		}
		var found bool
		value, found = object[name]
		if !found {
			return newDocumentValue(nil, false)
		}
	}
	return newDocumentValue(value, true)
}

func (p documentPath) delete(root any) (any, error) {
	return deleteDocumentValue(root, p.parts)
}

func (p documentPath) String() string {
	var output strings.Builder
	output.WriteByte('$')
	for _, part := range p.parts {
		if index, indexed := part.arrayIndex(); indexed {
			fmt.Fprintf(&output, "[%d]", index)
			continue
		}
		name, _ := part.fieldName()
		if isIdentifier(name) {
			output.WriteByte('.')
			output.WriteString(name)
			continue
		}
		fmt.Fprintf(&output, "[%q]", name)
	}
	return output.String()
}

type pathPart struct {
	name    string
	index   int
	indexed bool
}

func newFieldPathPart(name string) pathPart {
	return pathPart{name: name}
}

func newIndexPathPart(index int) pathPart {
	return pathPart{index: index, indexed: true}
}

func (p pathPart) arrayIndex() (index int, indexed bool) {
	return p.index, p.indexed
}

func (p pathPart) fieldName() (name string, named bool) {
	return p.name, !p.indexed
}

func (p pathPart) equals(other pathPart) bool {
	return p == other
}

// deleteDocumentValue mutates only maps and slices owned by its document.
func deleteDocumentValue(value any, parts []pathPart) (any, error) {
	part := parts[0]
	if index, indexed := part.arrayIndex(); indexed {
		array, ok := value.([]any)
		if !ok {
			if value == nil {
				return value, nil
			}
			return nil, errors.New("comparison path parent is not an array")
		}
		if index < 0 || index >= len(array) {
			return array, nil
		}
		if len(parts) == 1 {
			return append(array[:index], array[index+1:]...), nil
		}
		updated, err := deleteDocumentValue(array[index], parts[1:])
		if err != nil {
			return nil, err
		}
		array[index] = updated
		return array, nil
	}
	name, named := part.fieldName()
	if !named {
		return nil, errors.New("comparison path contains an invalid part")
	}
	object, ok := value.(map[string]any)
	if !ok {
		if value == nil {
			return value, nil
		}
		return nil, errors.New("comparison path parent is not an object")
	}
	child, present := object[name]
	if !present {
		return object, nil
	}
	if len(parts) == 1 {
		delete(object, name)
		return object, nil
	}
	updated, err := deleteDocumentValue(child, parts[1:])
	if err != nil {
		return nil, err
	}
	object[name] = updated
	return object, nil
}

func equalPathParts(left, right []pathPart) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !left[index].equals(right[index]) {
			return false
		}
	}
	return true
}

func isIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if character == '_' || character == '$' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

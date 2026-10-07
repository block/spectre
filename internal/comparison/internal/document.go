package comparisoninternal

import (
	"fmt"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
)

// document is one mutable normalisation copy; changing it never mutates captured data.
// Its root is None once a normaliser removes it.
type document struct {
	root Option[any]
}

func newDocument(root any) *document {
	return &document{root: Some(root)}
}

func (d *document) Value(path documentPath) documentValue {
	return path.value(d.root)
}

func (d *document) Delete(path documentPath) error {
	root, present := d.root.Get()
	if !present {
		return nil
	}
	if path.empty() {
		d.root = None[any]()
		return nil
	}
	updated, err := path.delete(root)
	if err != nil {
		return err
	}
	d.root = Some(updated)
	return nil
}

// Set replaces the value at path, whose parent must already exist.
func (d *document) Set(path documentPath, value any) error {
	if path.empty() {
		d.root = Some(value)
		return nil
	}
	root, present := d.root.Get()
	if !present {
		return errors.New("normalisation path parent is absent")
	}
	return path.set(root, value)
}

func (d *document) Export() Option[any] {
	return d.root
}

// documentValue distinguishes an absent path, None, from a present JSON null value.
type documentValue struct {
	value Option[any]
}

func newDocumentValue(value Option[any]) documentValue {
	return documentValue{value: value}
}

func (v documentValue) isPresent() bool {
	return v.value.Ok()
}

func (v documentValue) normaliserArgument() Option[any] {
	return v.value
}

func (v documentValue) array() ([]any, bool) {
	array, ok := v.value.Default(nil).([]any)
	return array, ok
}

func (v documentValue) object() (map[string]any, bool) {
	object, ok := v.value.Default(nil).(map[string]any)
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

func (p documentPath) value(root Option[any]) documentValue {
	absent := newDocumentValue(None[any]())
	value, present := root.Get()
	if !present {
		return absent
	}
	for _, part := range p.parts {
		if index, indexed := part.arrayIndex(); indexed {
			array, ok := value.([]any)
			if !ok || index < 0 || index >= len(array) {
				return absent
			}
			value = array[index]
			continue
		}
		name, named := part.fieldName()
		if !named {
			return absent
		}
		object, ok := value.(map[string]any)
		if !ok {
			return absent
		}
		var found bool
		value, found = object[name]
		if !found {
			return absent
		}
	}
	return newDocumentValue(Some(value))
}

func (p documentPath) delete(root any) (any, error) {
	return deleteDocumentValue(root, p.parts)
}

func (p documentPath) set(root any, value any) error {
	parent := p.parent().value(Some(root))
	if !parent.isPresent() {
		return errors.New("normalisation path parent is absent")
	}
	last := p.parts[len(p.parts)-1]
	if index, indexed := last.arrayIndex(); indexed {
		array, ok := parent.array()
		if !ok || index < 0 || index >= len(array) {
			return errors.New("normalisation path does not identify an array element")
		}
		array[index] = value
		return nil
	}
	name, _ := last.fieldName()
	object, ok := parent.object()
	if !ok {
		return errors.New("normalisation path parent is not an object")
	}
	object[name] = value
	return nil
}

func (p documentPath) parent() documentPath {
	return newDocumentPath(p.parts[:len(p.parts)-1])
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

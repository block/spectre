package comparisoninternal

import (
	"fmt"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/schema"
)

// document is one normalisation copy of a payload; its root is None once removed.
// A change checks the new value against its slot, so every value keeps its declared type.
type document struct {
	declared schema.Type
	root     Option[schema.Value]
}

func newDocument(root schema.Value) *document {
	return &document{declared: root.Type(), root: Some(root)}
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
		d.root = None[schema.Value]()
		return nil
	}
	updated, err := path.deleteFrom(root)
	if err != nil {
		return err
	}
	d.root = Some(updated)
	return nil
}

// Set replaces the value at path, whose parent must already exist.
func (d *document) Set(path documentPath, value schema.Value) error {
	if path.empty() {
		root, err := schema.As(d.declared, value)
		if err != nil {
			return errors.WithStack(err)
		}
		d.root = Some(root)
		return nil
	}
	root, present := d.root.Get()
	if !present {
		return errors.New("normalisation path parent is absent")
	}
	updated, err := path.setIn(root, value)
	if err != nil {
		return err
	}
	d.root = Some(updated)
	return nil
}

func (d *document) Export() Option[schema.Value] {
	return d.root
}

// documentValue distinguishes an absent path, None, from a present JSON null value.
type documentValue struct {
	value Option[schema.Value]
}

func newDocumentValue(value Option[schema.Value]) documentValue {
	return documentValue{value: value}
}

func (v documentValue) isPresent() bool {
	return v.value.Ok()
}

func (v documentValue) isNull() bool {
	value, present := v.value.Get()
	_, null := value.(schema.NullValue)
	return present && null
}

func (v documentValue) normaliserArgument() Option[schema.Value] {
	return v.value
}

func (v documentValue) list() (schema.ListValue, bool) {
	value, _ := v.value.Get()
	list, ok := value.(schema.ListValue)
	return list, ok
}

func (v documentValue) object() (schema.ObjectValue, bool) {
	value, _ := v.value.Get()
	object, ok := value.(schema.ObjectValue)
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

func (p documentPath) appendAnyKey() documentPath {
	return p.append(newAnyKeyPathPart())
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

func (p documentPath) value(root Option[schema.Value]) documentValue {
	absent := newDocumentValue(None[schema.Value]())
	value, present := root.Get()
	if !present {
		return absent
	}
	for _, part := range p.parts {
		if index, indexed := part.arrayIndex(); indexed {
			list, ok := value.(schema.ListValue)
			if !ok {
				return absent
			}
			if value, ok = list.Element(index); !ok {
				return absent
			}
			continue
		}
		name, named := part.fieldName()
		if !named {
			return absent
		}
		object, ok := value.(schema.ObjectValue)
		if !ok {
			return absent
		}
		if value, ok = object.Member(name); !ok {
			return absent
		}
	}
	return newDocumentValue(Some(value))
}

// reported returns the path as a difference reports it.
// deleteFrom returns root without the value at the path.
func (p documentPath) deleteFrom(root schema.Value) (schema.Value, error) {
	return deleteDocumentValue(root, p.parts)
}

// setIn returns root with value at the path.
func (p documentPath) setIn(root, value schema.Value) (schema.Value, error) {
	return setDocumentValue(root, p.parts, value)
}

func (p documentPath) reported() reportedPath {
	parts := make([]pathPart, len(p.parts))
	for i, part := range p.parts {
		parts[i] = part
		if _, indexed := part.arrayIndex(); indexed {
			parts[i] = newAnyKeyPathPart()
		}
	}
	return reportedPath{text: p.String(), wildcard: newDocumentPath(parts).String()}
}

// reportedPath is a difference's path, and the same path with each list index as [*].
type reportedPath struct {
	text     string
	wildcard string
}

func (p documentPath) String() string {
	var output strings.Builder
	output.WriteByte('$')
	for _, part := range p.parts {
		if index, indexed := part.arrayIndex(); indexed {
			fmt.Fprintf(&output, "[%d]", index)
			continue
		}
		if part.isAnyKey() {
			output.WriteString("[*]")
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
	// anyKey stands for an object key that is payload data, so a reported
	// difference path never quotes it. Only difference paths hold it.
	anyKey bool
}

func newFieldPathPart(name string) pathPart {
	return pathPart{name: name}
}

func newAnyKeyPathPart() pathPart {
	return pathPart{anyKey: true}
}

func newIndexPathPart(index int) pathPart {
	return pathPart{index: index, indexed: true}
}

func (p pathPart) arrayIndex() (index int, indexed bool) {
	return p.index, p.indexed
}

func (p pathPart) isAnyKey() bool {
	return p.anyKey
}

func (p pathPart) fieldName() (name string, named bool) {
	return p.name, !p.indexed && !p.anyKey
}

func (p pathPart) equals(other pathPart) bool {
	return p == other
}

// setDocumentValue returns value with the value at parts replaced, whose parent must exist.
func setDocumentValue(value schema.Value, parts []pathPart, replacement schema.Value) (schema.Value, error) {
	part := parts[0]
	if index, indexed := part.arrayIndex(); indexed {
		list, ok := value.(schema.ListValue)
		if !ok {
			return nil, errors.New("normalisation path does not identify an array element")
		}
		element, ok := list.Element(index)
		if !ok {
			return nil, errors.New("normalisation path does not identify an array element")
		}
		updated, err := replaced(element, parts[1:], replacement)
		if err != nil {
			return nil, err
		}
		return errors.WithStack2(list.With(index, updated))
	}
	name, _ := part.fieldName()
	object, ok := value.(schema.ObjectValue)
	if !ok {
		return nil, errors.New("normalisation path parent is not an object")
	}
	updated := replacement
	if len(parts) > 1 {
		child, present := object.Member(name)
		if !present {
			return nil, errors.New("normalisation path parent is absent")
		}
		var err error
		if updated, err = setDocumentValue(child, parts[1:], replacement); err != nil {
			return nil, err
		}
	}
	return errors.WithStack2(object.With(name, updated))
}

// replaced is replacement at the end of a path, or element with replacement below it.
func replaced(element schema.Value, rest []pathPart, replacement schema.Value) (schema.Value, error) {
	if len(rest) == 0 {
		return replacement, nil
	}
	return setDocumentValue(element, rest, replacement)
}

// deleteDocumentValue returns value without the value at parts. A path through null
// or past the end of a list leaves it as it is.
func deleteDocumentValue(value schema.Value, parts []pathPart) (schema.Value, error) {
	if _, null := value.(schema.NullValue); null {
		return value, nil
	}
	part := parts[0]
	if index, indexed := part.arrayIndex(); indexed {
		list, ok := value.(schema.ListValue)
		if !ok {
			return nil, errors.New("comparison path parent is not an array")
		}
		element, ok := list.Element(index)
		if !ok {
			return list, nil
		}
		if len(parts) == 1 {
			return list.Without(index), nil
		}
		updated, err := deleteDocumentValue(element, parts[1:])
		if err != nil {
			return nil, err
		}
		return errors.WithStack2(list.With(index, updated))
	}
	name, named := part.fieldName()
	if !named {
		return nil, errors.New("comparison path contains an invalid part")
	}
	object, ok := value.(schema.ObjectValue)
	if !ok {
		return nil, errors.New("comparison path parent is not an object")
	}
	child, present := object.Member(name)
	if !present {
		return object, nil
	}
	if len(parts) == 1 {
		return errors.WithStack2(object.Without(name))
	}
	updated, err := deleteDocumentValue(child, parts[1:])
	if err != nil {
		return nil, err
	}
	return errors.WithStack2(object.With(name, updated))
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

package schema_test

import (
	"math"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/schema"
)

var (
	itemType = &schema.ObjectType{Name: "m.Item", Fields: []schema.Field{
		{Name: "id", Type: schema.StringType{}},
	}}
	nullableString = schema.NullableType{Type: schema.StringType{}}
	ratioType      = schema.NumberType{Literals: []string{"NaN"}}
	kindType       = schema.EnumType{Name: Some(schema.TypeRef("m.Kind")), Literals: []string{"x", "y"}}
	rootType       = &schema.ObjectType{Name: "m.Root", Fields: []schema.Field{
		{Name: "name", Type: schema.StringType{}},
		{Name: "nick", Type: nullableString},
		{Name: "count", Optional: true, Type: schema.NumberType{}},
		{Name: "ratio", Optional: true, Type: ratioType},
		{Name: "flag", Optional: true, Type: schema.BooleanType{}},
		{Name: "kind", Optional: true, Type: kindType},
		{Name: "items", Optional: true, Type: schema.ListType{Element: itemType}},
		{Name: "byKey", Optional: true, Type: schema.MapType{Element: nullableString}},
		{Name: "child", Optional: true, Type: schema.NullableType{Type: itemType}},
		{Name: "extra", Optional: true, Type: schema.UnknownType{}},
	}}
)

func TestDecode(t *testing.T) {
	for name, test := range map[string]struct {
		declared schema.Type
		data     string
		want     string
	}{
		"String":             {declared: schema.StringType{}, data: ` "text" `, want: `"text"`},
		"Number":             {declared: schema.NumberType{}, data: `1.5`, want: `1.5`},
		"NumberLiteral":      {declared: ratioType, data: `"NaN"`, want: `"NaN"`},
		"Boolean":            {declared: schema.BooleanType{}, data: `false`, want: `false`},
		"Enum":               {declared: kindType, data: `"y"`, want: `"y"`},
		"NullableNull":       {declared: nullableString, data: `null`, want: `null`},
		"NullableString":     {declared: nullableString, data: `"a"`, want: `"a"`},
		"List":               {declared: schema.ListType{Element: schema.NumberType{}}, data: `[1, 2]`, want: `[1,2]`},
		"EmptyList":          {declared: schema.ListType{Element: schema.NumberType{}}, data: `[]`, want: `[]`},
		"NullableElements":   {declared: schema.ListType{Element: nullableString}, data: `["a",null]`, want: `["a",null]`},
		"MapKeys":            {declared: schema.MapType{Element: schema.NumberType{}}, data: `{"b":1,"":2,"a@b.c":3}`, want: `{"":2,"a@b.c":3,"b":1}`},
		"EmptyMap":           {declared: schema.MapType{Element: schema.NumberType{}}, data: `{}`, want: `{}`},
		"Unknown":            {declared: schema.UnknownType{}, data: `{"b":[1,null,"s",false],"a":{}}`, want: `{"a":{},"b":[1,null,"s",false]}`},
		"Object":             {declared: rootType, data: `{"nick":null,"name":"a"}`, want: `{"name":"a","nick":null}`},
		"OptionalAbsent":     {declared: rootType, data: `{"name":"a","nick":"n"}`, want: `{"name":"a","nick":"n"}`},
		"NestedObjects":      {declared: rootType, data: `{"name":"a","nick":null,"items":[{"id":"1"}],"child":null,"byKey":{"k":null}}`, want: `{"byKey":{"k":null},"child":null,"items":[{"id":"1"}],"name":"a","nick":null}`},
		"RepeatedKeyLastWon": {declared: schema.MapType{Element: schema.NumberType{}}, data: `{"a":1,"a":2}`, want: `{"a":2}`},
	} {
		t.Run(name, func(t *testing.T) {
			value, err := schema.Decode(test.declared, []byte(test.data))
			assert.NoError(t, err)
			encoded, err := value.MarshalJSON()
			assert.NoError(t, err)
			assert.Equal(t, test.want, string(encoded))
		})
	}
}

func TestDecodeRejects(t *testing.T) {
	for name, test := range map[string]struct {
		declared schema.Type
		data     string
		error    string
	}{
		"Empty":              {declared: schema.UnknownType{}, data: ``, error: "invalid JSON"},
		"Truncated":          {declared: schema.UnknownType{}, data: `{`, error: "invalid JSON at offset 1"},
		"SyntaxOffset":       {declared: schema.UnknownType{}, data: `{"a":}`, error: "invalid JSON at offset 6"},
		"TrailingValue":      {declared: schema.UnknownType{}, data: `1 2`, error: "invalid JSON"},
		"TrailingGarbage":    {declared: schema.UnknownType{}, data: `{} x`, error: "invalid JSON"},
		"OutOfRangeNumber":   {declared: schema.UnknownType{}, data: `1e999`, error: "invalid JSON"},
		"NullInPlainSlot":    {declared: schema.StringType{}, data: `null`, error: "$: expected string, found null"},
		"WrongScalar":        {declared: schema.BooleanType{}, data: `1`, error: "$: expected boolean, found a number"},
		"NumberString":       {declared: ratioType, data: `"Infinity"`, error: `$: expected number | "NaN", found a string`},
		"EnumLiteral":        {declared: kindType, data: `"z"`, error: `$: value is not one of m.Kind`},
		"EnumWrongJSON":      {declared: kindType, data: `1`, error: `$: expected m.Kind, found a number`},
		"NullableWrong":      {declared: nullableString, data: `1`, error: `$: expected string | null, found a number`},
		"ListElement":        {declared: schema.ListType{Element: schema.StringType{}}, data: `["a",1]`, error: `$[1]: expected string, found a number`},
		"ListNotArray":       {declared: schema.ListType{Element: schema.StringType{}}, data: `{}`, error: `$: expected string[], found an object`},
		"MapValue":           {declared: schema.MapType{Element: schema.StringType{}}, data: `{"secret":1}`, error: `$[*]: expected string, found a number`},
		"MissingRequired":    {declared: rootType, data: `{"nick":null}`, error: `$: required field "name" of type "m.Root" is missing`},
		"MissingNullable":    {declared: rootType, data: `{"name":"a"}`, error: `$: required field "nick" of type "m.Root" is missing`},
		"UndeclaredField":    {declared: rootType, data: `{"name":"a","nick":null,"secret":1}`, error: `$: type "m.Root" has a field it does not declare`},
		"NestedUndeclared":   {declared: rootType, data: `{"name":"a","nick":null,"items":[{"id":"1","x":2}]}`, error: `$.items[0]: type "m.Item" has a field it does not declare`},
		"NullOptionalNotNil": {declared: rootType, data: `{"name":"a","nick":null,"count":null}`, error: `$.count: expected number, found null`},
		"ObjectNotObject":    {declared: rootType, data: `[]`, error: `$: expected m.Root, found an array`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := schema.Decode(test.declared, []byte(test.data))
			assert.EqualError(t, err, test.error)
		})
	}
}

func TestDecodeRequiresType(t *testing.T) {
	_, err := schema.Decode(nil, []byte(`1`))
	assert.EqualError(t, err, `$: type is missing`)
}

func TestDecodeTypesEachNodeAsItsSlot(t *testing.T) {
	data := `{"name":"a","nick":"n","ratio":"NaN","kind":"x","items":[{"id":"1"}],"byKey":{"k":"v","n":null},` +
		`"child":null,"extra":{"list":[1,{"a":null}],"text":"t"}}`
	value, err := schema.Decode(rootType, []byte(data))
	assert.NoError(t, err)
	assert.Equal[schema.Type](t, rootType, value.Type())
	object := value.(schema.ObjectValue)
	items := member(t, object, "items").(schema.ListValue)
	byKey := member(t, object, "byKey").(schema.ObjectValue)
	extra := member(t, object, "extra").(schema.ObjectValue)
	list := member(t, extra, "list").(schema.ListValue)
	nested, _ := list.Element(1)
	for name, test := range map[string]struct {
		value schema.Value
		want  schema.Type
	}{
		"Required":            {value: member(t, object, "name"), want: schema.StringType{}},
		"NonNullInNullable":   {value: member(t, object, "nick"), want: nullableString},
		"NullInNullable":      {value: member(t, object, "child"), want: schema.NullableType{Type: itemType}},
		"NumberLiteral":       {value: member(t, object, "ratio"), want: ratioType},
		"Enum":                {value: member(t, object, "kind"), want: kindType},
		"List":                {value: items, want: schema.ListType{Element: itemType}},
		"ListElement":         {value: first(t, items), want: itemType},
		"ListElementMember":   {value: member(t, first(t, items).(schema.ObjectValue), "id"), want: schema.StringType{}},
		"Map":                 {value: byKey, want: schema.MapType{Element: nullableString}},
		"MapNonNullMember":    {value: member(t, byKey, "k"), want: nullableString},
		"MapNullMember":       {value: member(t, byKey, "n"), want: nullableString},
		"UnknownSlot":         {value: extra, want: schema.UnknownType{}},
		"UnknownList":         {value: list, want: schema.UnknownType{}},
		"UnknownListElement":  {value: nested, want: schema.UnknownType{}},
		"UnknownObjectMember": {value: member(t, extra, "text"), want: schema.UnknownType{}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.want, test.value.Type())
		})
	}
}

func TestAs(t *testing.T) {
	object, err := schema.NewObject(map[string]schema.Value{"id": schema.NewString("1")})
	assert.NoError(t, err)
	assert.Equal[schema.Type](t, schema.UnknownType{}, object.Type())
	typed, err := schema.As(itemType, object)
	assert.NoError(t, err)
	assert.Equal[schema.Type](t, itemType, typed.Type())
	assert.Equal[schema.Type](t, schema.StringType{}, member(t, typed.(schema.ObjectValue), "id").Type())
	assert.True(t, object.Equal(typed))
	// The original keeps its type.
	assert.Equal[schema.Type](t, schema.UnknownType{}, member(t, object.(schema.ObjectValue), "id").Type())

	_, err = schema.As(schema.NumberType{}, schema.NewString("a"))
	assert.EqualError(t, err, "$: expected number, found a string")
	_, err = schema.As(schema.StringType{}, nil)
	assert.EqualError(t, err, "value is missing")
	_, err = schema.As(itemType, schema.NewNull())
	assert.EqualError(t, err, "$: expected m.Item, found null")

	retyped, err := schema.As(schema.StringType{}, typed.(schema.ObjectValue).Members()["id"])
	assert.NoError(t, err)
	assert.Equal[schema.Type](t, schema.StringType{}, retyped.Type())
}

func TestEqualIgnoresTypes(t *testing.T) {
	number := func(n float64) schema.Value {
		value, err := schema.NewNumber(n)
		assert.NoError(t, err)
		return value
	}
	list := func(elements ...schema.Value) schema.Value {
		value, err := schema.NewList(elements)
		assert.NoError(t, err)
		return value
	}
	object := func(members map[string]schema.Value) schema.Value {
		value, err := schema.NewObject(members)
		assert.NoError(t, err)
		return value
	}
	decoded := func(declared schema.Type, data string) schema.Value {
		value, err := schema.Decode(declared, []byte(data))
		assert.NoError(t, err)
		return value
	}
	for name, test := range map[string]struct {
		left, right schema.Value
		equal       bool
	}{
		"TypedAndUnknown":    {left: decoded(rootType, `{"name":"a","nick":null}`), right: object(map[string]schema.Value{"name": schema.NewString("a"), "nick": schema.NewNull()}), equal: true},
		"NullableAndPlain":   {left: decoded(nullableString, `"a"`), right: decoded(schema.StringType{}, `"a"`), equal: true},
		"NullTypes":          {left: decoded(nullableString, `null`), right: schema.NewNull(), equal: true},
		"Numbers":            {left: number(1), right: decoded(schema.NumberType{}, `1.0`), equal: true},
		"DifferentNumbers":   {left: number(1), right: number(2)},
		"Strings":            {left: schema.NewString("a"), right: schema.NewString("b")},
		"Booleans":           {left: schema.NewBoolean(true), right: schema.NewBoolean(false)},
		"StringAndNumber":    {left: schema.NewString("1"), right: number(1)},
		"NullAndString":      {left: schema.NewNull(), right: schema.NewString("")},
		"ListOrder":          {left: list(number(1), number(2)), right: list(number(2), number(1))},
		"ListLength":         {left: list(number(1)), right: list(number(1), number(1))},
		"EqualLists":         {left: list(number(1), schema.NewNull()), right: decoded(schema.UnknownType{}, `[1,null]`), equal: true},
		"MemberValues":       {left: object(map[string]schema.Value{"a": number(1)}), right: object(map[string]schema.Value{"a": number(2)})},
		"MemberKeys":         {left: object(map[string]schema.Value{"a": number(1)}), right: object(map[string]schema.Value{"b": number(1)})},
		"KeyOrder":           {left: decoded(schema.UnknownType{}, `{"a":1,"b":2}`), right: decoded(schema.UnknownType{}, `{"b":2,"a":1}`), equal: true},
		"EmptyListAndObject": {left: list(), right: object(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.equal, test.left.Equal(test.right))
			assert.Equal(t, test.equal, test.right.Equal(test.left))
		})
	}
}

func TestMarshalJSON(t *testing.T) {
	object, err := schema.NewObject(map[string]schema.Value{"b": schema.NewBoolean(true), "a": schema.NewNull(), "c": schema.NewString("x")})
	assert.NoError(t, err)
	empty, err := schema.NewObject(nil)
	assert.NoError(t, err)
	list, err := schema.NewList(nil)
	assert.NoError(t, err)
	number, err := schema.NewNumber(-0.5)
	assert.NoError(t, err)
	for name, test := range map[string]struct {
		value schema.Value
		want  string
	}{
		"SortedKeys":      {value: object, want: `{"a":null,"b":true,"c":"x"}`},
		"EmptyObject":     {value: empty, want: `{}`},
		"EmptyList":       {value: list, want: `[]`},
		"ZeroList":        {value: schema.ListValue{}, want: `[]`},
		"ZeroObject":      {value: schema.ObjectValue{}, want: `{}`},
		"Number":          {value: number, want: `-0.5`},
		"Null":            {value: schema.NewNull(), want: `null`},
		"String":          {value: schema.NewString(`a"b`), want: `"a\"b"`},
		"Boolean":         {value: schema.NewBoolean(false), want: `false`},
		"ZeroNumberValue": {value: schema.NumberValue{}, want: `0`},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := test.value.MarshalJSON()
			assert.NoError(t, err)
			assert.Equal(t, test.want, string(encoded))
		})
	}
}

func TestConstructorsReject(t *testing.T) {
	for name, test := range map[string]struct {
		build func() (schema.Value, error)
		error string
	}{
		"NaN":              {build: func() (schema.Value, error) { return schema.NewNumber(math.NaN()) }, error: "number must be finite"},
		"PositiveInfinity": {build: func() (schema.Value, error) { return schema.NewNumber(math.Inf(1)) }, error: "number must be finite"},
		"NegativeInfinity": {build: func() (schema.Value, error) { return schema.NewNumber(math.Inf(-1)) }, error: "number must be finite"},
		"NilElement":       {build: func() (schema.Value, error) { return schema.NewList([]schema.Value{schema.NewNull(), nil}) }, error: "list element is missing"},
		"NilMember": {
			build: func() (schema.Value, error) { return schema.NewObject(map[string]schema.Value{"a": nil}) },
			error: "object member is missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			value, err := test.build()
			assert.EqualError(t, err, test.error)
			assert.Zero(t, value)
		})
	}
}

func TestValuesDoNotShareCallerState(t *testing.T) {
	elements := []schema.Value{schema.NewString("a")}
	list, err := schema.NewList(elements)
	assert.NoError(t, err)
	elements[0] = schema.NewString("changed")
	list.(schema.ListValue).Elements()[0] = schema.NewString("changed")
	assert.Equal(t, `["a"]`, marshal(t, list))

	members := map[string]schema.Value{"a": schema.NewString("a")}
	object, err := schema.NewObject(members)
	assert.NoError(t, err)
	members["a"] = schema.NewString("changed")
	object.(schema.ObjectValue).Members()["a"] = schema.NewString("changed")
	assert.Equal(t, `{"a":"a"}`, marshal(t, object))
}

func TestValueAccessors(t *testing.T) {
	value, err := schema.Decode(schema.UnknownType{}, []byte(`{"b":[true,1.5,"s"],"a":null}`))
	assert.NoError(t, err)
	object := value.(schema.ObjectValue)
	assert.Equal(t, 2, object.Len())
	assert.Equal(t, []string{"a", "b"}, object.Keys())
	_, ok := object.Member("missing")
	assert.False(t, ok)
	list := member(t, object, "b").(schema.ListValue)
	assert.Equal(t, 3, list.Len())
	_, ok = list.Element(3)
	assert.False(t, ok)
	_, ok = list.Element(-1)
	assert.False(t, ok)
	assert.True(t, first(t, list).(schema.BooleanValue).Bool())
	second, _ := list.Element(1)
	assert.Equal(t, 1.5, second.(schema.NumberValue).Float())
	third, _ := list.Element(2)
	assert.Equal(t, "s", third.(schema.StringValue).Text())
}

func TestWithKeepsDeclaredTypes(t *testing.T) {
	value, err := schema.Decode(rootType, []byte(`{"name":"a","nick":null,"items":[{"id":"1"}],"byKey":{}}`))
	assert.NoError(t, err)
	object := value.(schema.ObjectValue)
	items := member(t, object, "items").(schema.ListValue)
	byKey := member(t, object, "byKey").(schema.ObjectValue)
	for name, test := range map[string]struct {
		container func() (schema.Value, error)
		data      string
		member    func(container schema.Value) schema.Value
		want      schema.Type
	}{
		"Field": {
			container: func() (schema.Value, error) { return object.With("nick", schema.NewString("n")) },
			data:      `{"byKey":{},"items":[{"id":"1"}],"name":"a","nick":"n"}`,
			member:    func(container schema.Value) schema.Value { return member(t, container.(schema.ObjectValue), "nick") },
			want:      nullableString,
		},
		"MapMember": {
			container: func() (schema.Value, error) { return byKey.With("k", schema.NewString("v")) },
			data:      `{"k":"v"}`,
			member:    func(container schema.Value) schema.Value { return member(t, container.(schema.ObjectValue), "k") },
			want:      nullableString,
		},
		"ListElement": {
			container: func() (schema.Value, error) {
				return items.With(0, mustObject(t, map[string]schema.Value{"id": schema.NewString("2")}))
			},
			data:   `[{"id":"2"}]`,
			member: func(container schema.Value) schema.Value { return first(t, container.(schema.ListValue)) },
			want:   itemType,
		},
	} {
		t.Run(name, func(t *testing.T) {
			container, err := test.container()
			assert.NoError(t, err)
			assert.Equal(t, test.data, marshal(t, container))
			assert.Equal(t, test.want, test.member(container).Type())
		})
	}
	assert.Equal(t, `{"byKey":{},"items":[{"id":"1"}],"name":"a","nick":null}`, marshal(t, object))
}

func TestWithRejects(t *testing.T) {
	value, err := schema.Decode(rootType, []byte(`{"name":"a","nick":null,"items":[{"id":"1"}]}`))
	assert.NoError(t, err)
	object := value.(schema.ObjectValue)
	items := member(t, object, "items").(schema.ListValue)
	for name, test := range map[string]struct {
		with  func() (schema.Value, error)
		error string
	}{
		"FieldOfAnotherType":   {with: func() (schema.Value, error) { return object.With("name", schema.NewBoolean(true)) }, error: `$: expected string, found a boolean`},
		"NullInRequiredField":  {with: func() (schema.Value, error) { return object.With("name", schema.NewNull()) }, error: `$: expected string, found null`},
		"UndeclaredField":      {with: func() (schema.Value, error) { return object.With("secret", schema.NewString("s")) }, error: `type "m.Root" does not declare the field`},
		"ElementOfAnotherType": {with: func() (schema.Value, error) { return items.With(0, schema.NewString("s")) }, error: `$: expected m.Item, found a string`},
		"ElementOutOfRange":    {with: func() (schema.Value, error) { return items.With(1, schema.NewString("s")) }, error: `list has no element at the index`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := test.with()
			assert.EqualError(t, err, test.error)
		})
	}
}

// Without keeps the container's type even without a required field, as normalisers may remove one.
func TestWithoutKeepsDeclaredTypes(t *testing.T) {
	value, err := schema.Decode(rootType, []byte(`{"name":"a","nick":null,"items":[{"id":"1"},{"id":"2"}]}`))
	assert.NoError(t, err)
	object := value.(schema.ObjectValue)
	items := member(t, object, "items").(schema.ListValue)

	removed := object.Without("name")
	assert.Equal(t, `{"items":[{"id":"1"},{"id":"2"}],"nick":null}`, marshal(t, removed))
	assert.Equal[schema.Type](t, rootType, removed.Type())
	shortened := items.Without(0)
	assert.Equal(t, `[{"id":"2"}]`, marshal(t, shortened))
	assert.Equal[schema.Type](t, schema.ListType{Element: itemType}, shortened.Type())
	assert.Equal(t, `[{"id":"1"},{"id":"2"}]`, marshal(t, items.Without(2)))
	assert.Equal(t, `[{"id":"1"},{"id":"2"}]`, marshal(t, items))

	// Rebuilding the containers above a removed field keeps it removed.
	element := first(t, items).(schema.ObjectValue).Without("id")
	rebuiltItems, err := items.With(0, element)
	assert.NoError(t, err)
	rebuilt, err := object.With("items", rebuiltItems)
	assert.NoError(t, err)
	assert.Equal(t, `{"items":[{},{"id":"2"}],"name":"a","nick":null}`, marshal(t, rebuilt))
	assert.Equal[schema.Type](t, rootType, rebuilt.Type())
}

func mustObject(t *testing.T, members map[string]schema.Value) schema.Value {
	t.Helper()
	object, err := schema.NewObject(members)
	assert.NoError(t, err)
	return object
}

func member(t *testing.T, object schema.ObjectValue, key string) schema.Value {
	t.Helper()
	value, ok := object.Member(key)
	assert.True(t, ok, "member %q", key)
	return value
}

func first(t *testing.T, list schema.ListValue) schema.Value {
	t.Helper()
	value, ok := list.Element(0)
	assert.True(t, ok)
	return value
}

func marshal(t *testing.T, value schema.Value) string {
	t.Helper()
	encoded, err := value.MarshalJSON()
	assert.NoError(t, err)
	return string(encoded)
}

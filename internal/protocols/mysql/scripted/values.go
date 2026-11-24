package scripted

import (
	"strconv"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"
)

func valuesValue(runtime *sobek.Runtime, values []Option[string]) sobek.Value {
	elements := make([]sobek.Value, 0, len(values))
	for _, value := range values {
		elements = append(elements, nullable(runtime, value))
	}
	return arrayValue(runtime, elements)
}

func rowsValue(runtime *sobek.Runtime, rows []Row) sobek.Value {
	elements := make([]sobek.Value, 0, len(rows))
	for _, row := range rows {
		object := runtime.NewObject()
		for label, value := range row {
			_ = object.Set(label, nullable(runtime, value)) //nolint:errcheck // Plain objects accept any property.
		}
		elements = append(elements, object)
	}
	return arrayValue(runtime, elements)
}

// arrayValue returns a JavaScript array of the elements.
func arrayValue(runtime *sobek.Runtime, elements []sobek.Value) sobek.Value {
	array := runtime.NewArray()
	for index, element := range elements {
		_ = array.Set(strconv.Itoa(index), element) //nolint:errcheck // Arrays accept any index.
	}
	return array
}

func nullable(runtime *sobek.Runtime, value Option[string]) sobek.Value {
	if text, ok := value.Get(); ok {
		return runtime.ToValue(text)
	}
	return sobek.Null()
}

// statementValues reads the statement values a script passes as arguments.
func statementValues(arguments []sobek.Value) ([]Option[string], error) {
	values := make([]Option[string], 0, len(arguments))
	for _, argument := range arguments {
		text, err := sqlText(argument)
		if err != nil {
			return nil, err
		}
		values = append(values, text)
	}
	return values, nil
}

// readRows reads a script's array of row objects.
func readRows(value sobek.Value) ([]Row, error) {
	array, ok := value.(*sobek.Object)
	if !ok || array.ClassName() != "Array" {
		return nil, errors.New("rows must be an array")
	}
	length := array.Get("length").ToInteger()
	rows := make([]Row, 0, length)
	for index := range length {
		object, isObject := array.Get(strconv.FormatInt(index, 10)).(*sobek.Object)
		if !isObject || object.ClassName() != "Object" {
			return nil, errors.New("each row must be an object")
		}
		row := Row{}
		for _, label := range object.Keys() {
			text, err := sqlText(object.Get(label))
			if err != nil {
				return nil, errors.Wrapf(err, "row field %q", label)
			}
			row[label] = text
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// sqlText is a script value as MySQL's text protocol sends it, with null as NULL.
func sqlText(value sobek.Value) (Option[string], error) {
	switch value := value.Export().(type) {
	case nil:
		return None[string](), nil
	case string:
		return Some(value), nil
	case int64:
		return Some(strconv.FormatInt(value, 10)), nil
	case float64:
		return Some(strconv.FormatFloat(value, 'f', -1, 64)), nil
	case bool:
		if value {
			return Some("1"), nil
		}
		return Some("0"), nil
	}
	return None[string](), errors.Errorf("value of type %T is not a string, number, boolean or null", value.Export())
}

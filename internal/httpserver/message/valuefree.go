package message

import (
	"encoding/json"
	"strconv"

	"github.com/alecthomas/errors"
)

// Decode errors become quarantine reasons, which leave the mirror, so they must
// never quote payload values. These replace errors whose text quotes their input.

// jsonError keeps only where a JSON syntax error occurred.
func jsonError(err error) error {
	if syntax, ok := errors.AsType[*json.SyntaxError](err); ok {
		return errors.Errorf("invalid JSON at offset %d", syntax.Offset)
	}
	return errors.New("invalid JSON")
}

// numberError keeps strconv's cause but drops the text it failed to parse.
func numberError(err error) error {
	if number, ok := errors.AsType[*strconv.NumError](err); ok {
		return errors.Errorf("%s: %w", number.Func, number.Err)
	}
	return errors.New("invalid number")
}

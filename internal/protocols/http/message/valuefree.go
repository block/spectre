package message

import (
	"strconv"

	"github.com/alecthomas/errors"
)

// Decode errors become quarantine reasons, which leave the mirror, so they must
// never quote payload values. These replace errors whose text quotes their input.

// numberError keeps strconv's cause but drops the text it failed to parse.
func numberError(err error) error {
	if number, ok := errors.AsType[*strconv.NumError](err); ok {
		return errors.Errorf("%s: %w", number.Func, number.Err)
	}
	return errors.New("invalid number")
}

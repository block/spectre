package fixture

import (
	"errors"
	"net/http"
	"net/url"

	"example.com/fixture/generated"
)

type value struct{}

type named interface {
	name() string
}

func (*value) name() string { return "" }

var _ named = (*value)(nil)

type node struct {
	next *node
}

type holder struct {
	field *value
	items []int
	count int
}

func find() *value {
	return nil // want `nil returned; use Option`
}

func converted() (*value, *value) {
	return (*value)(nil), (nil) // want `nil returned; use Option` `nil returned; use Option`
}

func findWithoutError() (*value, error) {
	return nil, nil // want `nil returned; use Option`
}

func load() (*value, error) {
	return nil, errors.New("failed")
}

func lookup() (*value, bool) {
	return nil, false
}

func succeed() error {
	return nil
}

func dynamic() any {
	return nil
}

func last() *node {
	return nil
}

func empty() []int {
	return nil
}

func allowed() *value {
	return nil //optionalnil:allow The fixture needs a kept nil.
}

func allowedAbove() *value {
	//optionalnil:allow The fixture needs a kept nil.
	return nil
}

func use(target *value) {}

func useAll(targets ...*value) {}

func stores(h *holder, message *generated.Message) {
	h.field = nil // want `nil stored in field; use Option`
	_ = holder{field: nil} // want `nil stored in field; use Option`
	_ = holder{nil, nil, 0} // want `nil stored in field; use Option`
	var unset *value = nil // want `nil stored in unset; use Option`
	_ = unset
	h.items = nil
	_ = map[string]*value{"missing": nil} // want `nil stored in map; use Option`
	values := map[string]*value{}
	values["missing"] = nil // want `nil stored in map; use Option`
	_ = map[string][]int{"empty": nil}
	_ = generated.Registry{"missing": nil}
	message.Field = nil
	_ = http.Server{Handler: nil}
}

func arguments() {
	use(nil) // want `nil passed to use; use Option`
	useAll(nil) // want `nil passed to useAll; use Option`
	use((*value)(nil)) // want `nil passed to use; use Option`
	generated.Send(nil)
	_, _ = http.NewRequest(http.MethodGet, "/", nil)
}

func comparisons(h *holder, v any) error {
	if h.field != nil { // want `nil compared with field; use Option`
		return nil
	}
	if h.items == nil { // want `nil compared with items; use Option`
		return nil
	}
	var later *value
	if later != nil { // want `nil compared with later; use Option`
		return nil
	}
	if found := find(); found != nil { // want `nil compared with found; use Option`
		return nil
	}
	parsed, err := url.Parse("/")
	if err != nil || parsed != nil {
		return nil
	}
	if recovered := recover(); recovered != nil {
		return nil
	}
	if v == nil {
		return nil
	}
	if h == nil || h.field == nil {
		return errors.New("a field is required")
	}
	return nil
}

package example

type Data struct {
	Value int
}

var packageData = Data{Value: 1}

type PackageValue struct {
	value int
}

func New(value int) *PackageValue {
	return &PackageValue{value: value}
}

type Widget struct {
	value  int
	Visible int
}

func NewWidget(value int) *Widget {
	widget := &Widget{}
	widget.value = value
	return widget
}

func newWidget(value int) Widget {
	return Widget{value: value}
}

func parseWidget(value int) Widget {
	widget := Widget{}
	widget.value = value
	return widget
}

func (w *Widget) Value() int {
	return w.value
}

func (w Widget) OtherValue(other Widget) int {
	return other.value
}

func readWidget(widget *Widget) int {
	return widget.value // want "private field value may only be accessed by methods of Widget"
}

func buildValue() *Widget {
	return &Widget{} // want "Widget may only be constructed by New or a constructor ending in Widget"
}

var allocatedWidget = new(Widget) // want "Widget may only be constructed by New or a constructor ending in Widget"

type Other struct {
	value int
}

func NewOther() *Other {
	return &Other{}
}

func (o Other) ReadWidget(widget Widget) int {
	return widget.value // want "private field value may only be accessed by methods of Widget"
}

type Outer struct {
	Widget
}

func (o Outer) Value() int {
	return o.value // want "private field value may only be accessed by methods of Widget"
}

type Box[T any] struct {
	value T
}

func NewBox[T any](value T) *Box[T] {
	return &Box[T]{value: value}
}

func (b Box[T]) Value() T {
	return b.value
}

func buildGeneric() Box[int] {
	return Box[int]{} // want "Box may only be constructed by New or a constructor ending in Box"
}

type secret struct {
	value int
}

func newSecret(value int) secret {
	return secret{value: value}
}

func readSecret(value secret) int {
	return value.value
}

func buildSecret() secret {
	return secret{}
}

type privateCounter struct {
	value int
}

func newPrivateCounter(value int) privateCounter {
	return privateCounter{value: value}
}

func (c privateCounter) Value() int {
	return c.value
}

func readPrivateCounter(counter privateCounter) int {
	return counter.value // want "private field value may only be accessed by methods of privateCounter"
}

func buildCounter() privateCounter {
	return privateCounter{} // want "privateCounter may only be constructed by New or a constructor ending in PrivateCounter"
}

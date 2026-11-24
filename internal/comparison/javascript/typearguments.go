package javascript

// TypeArguments says how compile reads a generic method's type arguments.
type TypeArguments int

const (
	// TypeArgumentsSchema name schema types, which compile passes at run time. It is the default.
	TypeArgumentsSchema TypeArguments = iota
	// TypeArgumentsBody are TypeArgumentsSchema, except that the first may also be Text or unknown.
	TypeArgumentsBody
	// TypeArgumentsCheckOnly only check scripts, and are not passed at run time.
	TypeArgumentsCheckOnly
)

// TextTypeName and UnknownTypeName are the type names compile passes, for TypeArgumentsBody, in
// place of the spectre Text type and unknown. Schema type names never collide, as they contain a module name.
const (
	TextTypeName    = "spectre.Text"
	UnknownTypeName = "unknown"
)

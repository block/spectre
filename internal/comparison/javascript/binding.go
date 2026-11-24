package javascript

import (
	"github.com/alecthomas/errors"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/model"
)

// Binding is one protocol's part of the spectre script module. Build it with NewBinding.
type Binding struct {
	protocol      model.Protocol
	members       string
	payloads      string
	typeArguments map[string]TypeArguments
	build         func(scope *Scope) (boundMembers, error)
}

// NewBinding binds a protocol, which define installs as ingress.<protocol> and egress.<protocol>. The
// typeArguments, keyed by owner and method like "IngressRule.match", list methods that differ from the default.
func NewBinding[D, I any](
	protocol model.Protocol,
	members, payloads string,
	typeArguments map[string]TypeArguments,
	define func(scope *Scope) (Members[D, I], error),
) (Binding, Handle[D, I]) {
	id := &bindingID{}
	return Binding{
		protocol:      protocol,
		members:       members,
		payloads:      payloads,
		typeArguments: typeArguments,
		build: func(scope *Scope) (boundMembers, error) {
			defined, err := define(scope)
			return newBound(id, defined), errors.WithStack(err)
		},
	}, newHandle[D, I](id)
}

// NewEmptyBinding binds a protocol that scripts cannot declare traffic for. It adds
// nothing to the script module, but still claims the protocol's name.
func NewEmptyBinding(protocol model.Protocol) Binding {
	binding, _ := NewBinding(protocol, "", "", nil, func(*Scope) (Members[struct{}, struct{}], error) {
		return Members[struct{}, struct{}]{Declared: func() struct{} { return struct{}{} }}, nil
	})
	return binding
}

// Protocol returns the protocol the binding adds to ingress and egress.
func (b Binding) Protocol() model.Protocol {
	return b.protocol
}

// Members is TypeScript adding the protocol's members to the spectre module's
// Ingress and Egress interfaces.
func (b Binding) Members() string {
	return b.members
}

// Payloads is TypeScript declaring the spectre.<name> modules of the payloads
// the protocol creates.
func (b Binding) Payloads() string {
	return b.payloads
}

// typeArgumentsOf says how compile reads the type arguments of a method, named by owner and method.
func (b Binding) typeArgumentsOf(method string) TypeArguments {
	return b.typeArguments[method]
}

// defineIn defines the binding's members in a runtime and adds them to the shared ingress and egress.
func (b Binding) defineIn(scope *Scope, ingress, egress *sobek.Object) (boundMembers, error) {
	members, err := b.build(scope)
	if err == nil {
		err = members.install(b.protocol, ingress, egress)
	}
	return members, errors.Wrap(err, "define script members")
}

// claim records the binding's protocol in protocols, rejecting a protocol that another
// binding has claimed. Two bindings declaring a payload module fail when the schema is read.
func (b Binding) claim(protocols map[model.Protocol]bool) error {
	if b.build == nil {
		return errors.New("binding was not built with NewBinding")
	}
	if protocols[b.protocol] {
		return errors.Errorf("protocol %q has more than one script binding", b.protocol)
	}
	protocols[b.protocol] = true
	return nil
}

// checkBindings rejects bindings that cannot be used together.
func checkBindings(bindings []Binding) error {
	protocols := map[model.Protocol]bool{}
	for _, binding := range bindings {
		if err := binding.claim(protocols); err != nil {
			return err
		}
	}
	return nil
}

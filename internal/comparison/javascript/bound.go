package javascript

import (
	"reflect"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/model"
)

// bindingID identifies the binding that built a Handle, a snapshot or a bound members.
// It is a pointer so no two bindings share it, and it is not zero-sized so that holds.
type bindingID struct{ _ byte }

// boundMembers is a binding's Members with their types erased, so a runtime can hold
// every binding's members alike.
type boundMembers interface {
	// install adds the members to the shared ingress and egress objects.
	install(protocol model.Protocol, ingress, egress *sobek.Object) error
	snapshot() declaredSnapshot
}

// declaredSnapshot is what one binding's scripts declared, with its type erased.
type declaredSnapshot interface {
	// sameAs reports whether other holds equal declarations of the same binding.
	sameAs(other declaredSnapshot) bool
}

// bound is the boundMembers of the binding with id.
type bound[D, I any] struct {
	id      *bindingID
	members Members[D, I]
}

func newBound[D, I any](id *bindingID, members Members[D, I]) bound[D, I] {
	return bound[D, I]{id: id, members: members}
}

// instanceOf returns the instance if the binding has id.
func (b bound[D, I]) instanceOf(id *bindingID) (I, bool) {
	return b.members.Instance, b.id == id
}

func (b bound[D, I]) install(protocol model.Protocol, ingress, egress *sobek.Object) error {
	if b.members.Declared == nil {
		return errors.New("members have no Declared function")
	}
	if err := installMember(protocol, ingress, b.members.Ingress); err != nil {
		return errors.Wrap(err, "ingress")
	}
	return errors.Wrap(installMember(protocol, egress, b.members.Egress), "egress")
}

func (b bound[D, I]) snapshot() declaredSnapshot {
	return newSnapshot(b.id, b.members.Declared())
}

// installMember adds a binding's member to a shared object as the protocol's property.
func installMember(protocol model.Protocol, target *sobek.Object, member Option[sobek.Value]) error {
	value, ok := member.Get()
	if !ok {
		return nil
	}
	return errors.WithStack(setMember(target, string(protocol), value))
}
func setMember(target *sobek.Object, name string, value sobek.Value) error {
	if target.Get(name) != nil {
		return errors.Errorf("member %q is defined by more than one binding", name)
	}
	return errors.Wrapf(target.Set(name, value), "define member %q", name)
}

// snapshotOf is the declaredSnapshot of the binding with id.
type snapshotOf[D any] struct {
	id    *bindingID
	value D
}

func newSnapshot[D any](id *bindingID, value D) snapshotOf[D] {
	return snapshotOf[D]{id: id, value: value}
}

// declaredBy returns the declarations if the binding has id.
func (s snapshotOf[D]) declaredBy(id *bindingID) (D, bool) {
	return s.value, s.id == id
}

func (s snapshotOf[D]) sameAs(other declaredSnapshot) bool {
	same, ok := other.(snapshotOf[D])
	return ok && same.id == s.id && reflect.DeepEqual(s.value, same.value)
}

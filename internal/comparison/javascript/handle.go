package javascript

// Handle reads a binding's declarations and per-runtime instances. Only the
// protocol that built the binding holds it.
type Handle[D, I any] struct {
	id *bindingID
}

func newHandle[D, I any](id *bindingID) Handle[D, I] {
	return Handle[D, I]{id: id}
}

// Declared returns what the program's scripts declared through the binding, which is
// the zero value if the program was not built with it. Callers must not modify it.
//
//nolint:narrowdeps // The binding reads declarations from the program that loaded them.
func (h Handle[D, I]) Declared(program *Program) D {
	for _, declared := range program.declarations() {
		if snapshot, ok := declared.(snapshotOf[D]); ok {
			if value, mine := snapshot.declaredBy(h.id); mine {
				return value
			}
		}
	}
	var none D
	return none
}

// Instance returns the binding's instance in the evaluator's runtime, which is the
// zero value if the evaluator was not built with it.
//
//nolint:narrowdeps // The binding reads its instance from the evaluator that holds it.
func (h Handle[D, I]) Instance(evaluator *Evaluator) I {
	for _, member := range evaluator.boundMembers() {
		if members, ok := member.(bound[D, I]); ok {
			if instance, mine := members.instanceOf(h.id); mine {
				return instance
			}
		}
	}
	var none I
	return none
}

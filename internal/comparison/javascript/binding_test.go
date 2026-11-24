package javascript_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"github.com/grafana/sobek"

	"github.com/block/spectre/internal/comparison/javascript"
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

const kafkaMembers = `
declare module "spectre" {
  export interface Ingress {
    /** Starts a rule for a topic. */
    kafka(topic: string): KafkaTopic;
  }
  export interface KafkaTopic {
    handle<T extends object>(normalise: (message: T) => T): void;
    describe<T>(): void;
    body<T>(): void;
  }
}
`

const kafkaPayloads = `declare module "spectre.kafka" { interface Record { key: string } }`

// kafkaDeclared is what scripts declared through the test binding.
type kafkaDeclared struct {
	topics []string
	// types are the type names that handle received, and bodies those that body received.
	types  []schema.TypeRef
	bodies []string
	// describes counts the arguments each describe call received.
	describes []int
}

// kafkaInstance holds one runtime's handlers, indexed like the declared topics.
type kafkaInstance struct {
	scope    *javascript.Scope
	handlers []sobek.Callable
}

type kafka struct {
	binding javascript.Binding
	handle  javascript.Handle[kafkaDeclared, *kafkaInstance]
}

// newKafka is a binding of a protocol that proxies ingress only. Its members are named for
// the protocol, so two of them can share a program.
func newKafka(protocol model.Protocol, payloads string) kafka {
	owner := strings.ToUpper(string(protocol[:1])) + string(protocol[1:]) + "Topic"
	members := strings.NewReplacer("kafka(", string(protocol)+"(", "KafkaTopic", owner).Replace(kafkaMembers)
	binding, handle := javascript.NewBinding(
		protocol,
		members,
		payloads,
		map[string]javascript.TypeArguments{
			owner + ".describe": javascript.TypeArgumentsCheckOnly,
			owner + ".body":     javascript.TypeArgumentsBody,
		},
		func(scope *javascript.Scope) (javascript.Members[kafkaDeclared, *kafkaInstance], error) {
			runtime := scope.Runtime()
			must := func(err error) {
				if err != nil {
					panic(err)
				}
			}
			declared := &kafkaDeclared{}
			instance := &kafkaInstance{scope: scope}
			topic := func(call sobek.FunctionCall) sobek.Value {
				name := call.Argument(0).String()
				object := runtime.NewObject()
				must(object.Set("handle", func(call sobek.FunctionCall) sobek.Value {
					typeName := schema.TypeRef(call.Argument(0).String())
					handler, _ := sobek.AssertFunction(call.Argument(1))
					scope.DeclareEndpoint(javascript.NewEndpoint(model.Ingress, protocol, name, typeName, false))
					declared.topics = append(declared.topics, name)
					declared.types = append(declared.types, typeName)
					instance.handlers = append(instance.handlers, handler)
					return sobek.Undefined()
				}))
				must(object.Set("describe", func(call sobek.FunctionCall) sobek.Value {
					declared.describes = append(declared.describes, len(call.Arguments))
					return sobek.Undefined()
				}))
				must(object.Set("body", func(call sobek.FunctionCall) sobek.Value {
					declared.bodies = append(declared.bodies, call.Argument(0).String())
					return sobek.Undefined()
				}))
				return object
			}
			return javascript.Members[kafkaDeclared, *kafkaInstance]{
				Ingress: Some(runtime.ToValue(topic)),
				Declared: func() kafkaDeclared {
					return *declared
				},
				Instance: instance,
			}, nil
		},
	)
	return kafka{binding: binding, handle: handle}
}

func kafkaProgram(t *testing.T, bindings []javascript.Binding, body string) (*javascript.Program, error) {
	t.Helper()
	files := fstest.MapFS{"test.ts": script(body)}
	return javascript.NewProgram(t.Context(), time.Second, declarations(), files, nil, bindings)
}

func TestBindingDeclaresAndInstantiates(t *testing.T) {
	broker := newKafka("kafka", kafkaPayloads)
	program, err := kafkaProgram(t, []javascript.Binding{broker.binding}, `
		spectre.ingress.kafka("orders").handle<v1.User>((message) => ({ name: message.name + "!" }));
		spectre.ingress.kafka("any").describe<v1.User>();
		spectre.ingress.kafka("any").body<spectre.Text>();
		spectre.ingress.kafka("any").body<unknown>();
		spectre.ingress.kafka("any").body<v1.User>();
	`)
	assert.NoError(t, err)

	assert.Equal(t, kafkaDeclared{
		topics:    []string{"orders"},
		types:     []schema.TypeRef{"test.v1.User"},
		bodies:    []string{"spectre.Text", "unknown", "test.v1.User"},
		describes: []int{0},
	}, broker.handle.Declared(program))
	assert.Equal(t, []endpoint{{"kafka", "orders", "test.v1.User"}}, endpoints(program, model.Ingress))
	_, err = program.Schema().Type("spectre.kafka.Record")
	assert.NoError(t, err)

	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	instance := broker.handle.Instance(evaluator)
	assert.Equal(t, 1, len(instance.handlers))
	result, err := instance.scope.Call(instance.handlers[0], objectValue(t, program, "test.v1.User", `{"name":"alice"}`))
	assert.NoError(t, err)
	assert.Equal(t, Some(unknownValue(t, `{"name":"alice!"}`)), result)
}

func TestHandleReadsOnlyItsBinding(t *testing.T) {
	broker := newKafka("kafka", kafkaPayloads)
	other := newKafka("other", "")
	program, err := kafkaProgram(t, []javascript.Binding{broker.binding, other.binding}, `
		spectre.ingress.kafka("orders").handle<v1.User>((message) => message);
		spectre.ingress.other("returns").handle<v1.User>((message) => message);
	`)
	assert.NoError(t, err)
	assert.Equal(t, []string{"orders"}, broker.handle.Declared(program).topics)
	assert.Equal(t, []string{"returns"}, other.handle.Declared(program).topics)

	withoutBroker, err := kafkaProgram(t, nil, ``)
	assert.NoError(t, err)
	assert.Equal(t, kafkaDeclared{}, broker.handle.Declared(withoutBroker))
	evaluator, err := withoutBroker.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	assert.Equal(t, (*kafkaInstance)(nil), broker.handle.Instance(evaluator))
}

func TestBindingTypeChecksScripts(t *testing.T) {
	broker := newKafka("kafka", kafkaPayloads)
	for name, test := range map[string]struct {
		bindings []javascript.Binding
		body     string
		message  string
	}{
		"MissingBinding": {
			body:    `spectre.ingress.kafka("orders");`,
			message: "Property 'kafka' does not exist on type 'Ingress'",
		},
		"WrongDirection": {
			bindings: []javascript.Binding{broker.binding},
			body:     `spectre.egress.kafka("orders");`,
			message:  "Property 'kafka' does not exist on type 'Egress'",
		},
		"MissingTypeArgument": {
			bindings: []javascript.Binding{broker.binding},
			body:     `spectre.ingress.kafka("orders").handle((message: v1.User) => message);`,
			message:  "spectre.KafkaTopic.handle needs 1 explicit type argument(s)",
		},
		"TextOutsideBody": {
			bindings: []javascript.Binding{broker.binding},
			body:     `spectre.ingress.kafka("orders").handle<spectre.Text>((message) => message);`,
			message:  "only ingress rules may compare bodies as Text",
		},
		"UnknownOutsideBody": {
			bindings: []javascript.Binding{broker.binding},
			body:     `spectre.ingress.kafka("orders").handle<unknown>((message) => message);`,
			message:  "error TS",
		},
		"DuplicateEndpoint": {
			bindings: []javascript.Binding{broker.binding},
			body: `spectre.ingress.kafka("orders").handle<v1.User>((message) => message);
				spectre.ingress.kafka("orders").handle<v1.User>((message) => message);`,
			message: `duplicate endpoint "orders"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := kafkaProgram(t, test.bindings, test.body)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestRejectsConflictingBindings(t *testing.T) {
	broker := newKafka("kafka", kafkaPayloads)
	for name, test := range map[string]struct {
		bindings []javascript.Binding
		message  string
	}{
		"SamePayloadModule": {
			bindings: []javascript.Binding{broker.binding, newKafka("other", kafkaPayloads).binding},
			message:  `payload module "spectre.kafka" is declared more than once`,
		},
		"SameProtocol": {
			bindings: []javascript.Binding{broker.binding, newKafka("kafka", "").binding},
			message:  `protocol "kafka" has more than one script binding`,
		},
		"UnnamedPayloadModule": {
			bindings: []javascript.Binding{newKafka("kafka", `declare module "kafka" { interface Record {} }`).binding},
			message:  `payload module "kafka" must be named under "spectre."`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := kafkaProgram(t, test.bindings, ``)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestReEvaluationMustReproduceBindingDeclarations(t *testing.T) {
	runs := 0
	binding, _ := javascript.NewBinding(
		"unstable", "", "", nil,
		func(*javascript.Scope) (javascript.Members[int, struct{}], error) {
			runs++
			return javascript.Members[int, struct{}]{Declared: func() int { return runs }}, nil
		},
	)
	program, err := kafkaProgram(t, []javascript.Binding{binding}, ``)
	assert.NoError(t, err)
	_, err = program.NewEvaluator(t.Context())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "registrations changed after startup")
}

func TestModuleDeclarationIncludesBindings(t *testing.T) {
	broker := newKafka("kafka", kafkaPayloads)
	declaration := javascript.ModuleDeclaration([]javascript.Binding{broker.binding})
	shared := javascript.ModuleDeclaration(nil)
	assert.Equal(t, shared+"\n"+kafkaMembers+"\n"+kafkaPayloads, declaration)
}

const timedMembers = `
declare module "spectre" {
  export interface Ingress {
    timed: {
      /** Registers a callback that the test runs. */
      register(run: () => void): void;
      /** Waits without counting the time against the callback's timeout. */
      wait(milliseconds: number): void;
    };
  }
}
`

// timedInstance holds one runtime's callbacks of the timed binding.
type timedInstance struct {
	scope     *javascript.Scope
	callbacks []sobek.Callable
}

func newTimed() (javascript.Binding, javascript.Handle[struct{}, *timedInstance]) {
	return javascript.NewBinding(
		"timed", timedMembers, "", nil,
		func(scope *javascript.Scope) (javascript.Members[struct{}, *timedInstance], error) {
			runtime := scope.Runtime()
			instance := &timedInstance{scope: scope}
			object := runtime.NewObject()
			must := func(err error) {
				if err != nil {
					panic(err)
				}
			}
			must(object.Set("register", func(call sobek.FunctionCall) sobek.Value {
				callback, _ := sobek.AssertFunction(call.Argument(0))
				instance.callbacks = append(instance.callbacks, callback)
				return sobek.Undefined()
			}))
			must(object.Set("wait", func(call sobek.FunctionCall) sobek.Value {
				pause := time.Duration(call.Argument(0).ToInteger()) * time.Millisecond
				if err := scope.Wait(func() error { time.Sleep(pause); return nil }); err != nil {
					panic(runtime.NewGoError(err))
				}
				return sobek.Undefined()
			}))
			return javascript.Members[struct{}, *timedInstance]{
				Ingress:  Some[sobek.Value](object),
				Declared: func() struct{} { return struct{}{} },
				Instance: instance,
			}, nil
		},
	)
}

// A callback's time counts against the program's timeout, except while it waits.
func TestCallbacksRunUnderTheTimeoutWithoutWaiting(t *testing.T) {
	binding, handle := newTimed()
	files := fstest.MapFS{"test.ts": script(`
		spectre.ingress.timed.register(() => { spectre.ingress.timed.wait(300); });
		spectre.ingress.timed.register(() => { for (;;) {} });
	`)}
	program, err := javascript.NewProgram(t.Context(), 100*time.Millisecond, declarations(), files, nil, []javascript.Binding{binding})
	assert.NoError(t, err)
	evaluator, err := program.NewEvaluator(t.Context())
	assert.NoError(t, err)
	defer evaluator.Close()
	instance := handle.Instance(evaluator)

	_, err = instance.scope.Invoke(instance.callbacks[0], "callback threw an exception", readNothing)
	assert.NoError(t, err)
	_, err = instance.scope.Invoke(instance.callbacks[1], "callback threw an exception", readNothing)
	assert.IsError(t, err, context.DeadlineExceeded)
}

func readNothing(sobek.Value) (struct{}, error) {
	return struct{}{}, nil
}

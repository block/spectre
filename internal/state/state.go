package state

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/block/spectre/internal/model"
)

// Status says where a node's code lives.
type Status string

const (
	// Pending code is only in the reference.
	Pending Status = "pending"
	// Migrated code is in the candidate as well as the reference.
	Migrated Status = "migrated"
)

// State is a service's migration state.
type State struct {
	// Nodes are the service's modules, sorted by ID.
	Nodes []Node
}

// Node is one module of the service.
type Node struct {
	ID        string   `toml:"-" json:"id"`
	DependsOn []string `toml:"depends_on,omitempty" json:"depends_on,omitzero"`
	Status    Status   `toml:"status,omitempty" json:"status"`
	Ingress   Ingress  `toml:"ingress,omitempty" json:"ingress,omitzero"`
	Egress    Egress   `toml:"egress,omitempty" json:"egress,omitzero"`
	// Line is where the node's table starts in the file, or 0 when unknown.
	Line int `toml:"-" json:"-"`
}

// Ingress is the IO that reaches a node, with a field per protocol. Fields are
// pointers only because go-toml cannot decode an Option; read them as Options.
type Ingress struct {
	HTTP *HTTPIngress `toml:"http,omitempty" json:"http,omitzero"`
}

// HTTPSettings returns the node's ingress[http] settings, if it has that kind of IO.
func (i Ingress) HTTPSettings() Option[HTTPIngress] {
	return Ptr(i.HTTP)
}

// Egress is the IO a node performs, with a field per protocol. Fields are
// pointers only because go-toml cannot decode an Option; read them as Options.
type Egress struct {
	HTTP *HTTPEgress `toml:"http,omitempty" json:"http,omitzero"`
}

// HTTPSettings returns the node's egress[http] settings, if it has that kind of IO.
func (e Egress) HTTPSettings() Option[HTTPEgress] {
	return Ptr(e.HTTP)
}

// HTTPIngress is a node's ingress[http] settings.
type HTTPIngress struct {
	// Routes are unique across nodes, and there must be at least one.
	Routes map[model.Route]HTTPRoute `toml:"routes" json:"routes"`
}

// HTTPRoute is the settings of one route a node handles, which has none yet.
type HTTPRoute struct{}

// HTTPEgress is a node's egress[http] settings, which has none yet.
type HTTPEgress struct{}

// file is the layout of a state file, which go-toml decodes and encodes directly.
type file struct {
	Nodes map[string]Node `toml:"nodes,omitempty"`
}

// Problem is one thing wrong with a migration state.
type Problem struct {
	// Line is where the problem is in the file, or 0 when unknown.
	Line    int
	Message string
}

// String returns the problem prefixed with its line, if known.
func (p Problem) String() string {
	if p.Line == 0 {
		return p.Message
	}
	return fmt.Sprintf("line %d: %s", p.Line, p.Message)
}

// InvalidError lists every problem that makes a migration state invalid.
type InvalidError struct {
	Problems []Problem
}

// Error returns the first problem and how many more there are.
func (e *InvalidError) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0].String()
	}
	return fmt.Sprintf("%s (and %d more problems)", e.Problems[0], len(e.Problems)-1)
}

// Keys of the state file that problems name.
const (
	nodesKey     = "nodes"
	dependsOnKey = "depends_on"
	statusKey    = "status"
)

var nodeIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Config locates the migration state file.
type Config struct {
	// StateFile is the path to the migration state TOML file.
	StateFile string `default:"spectre/state.toml" type:"path" help:"Path to the migration state TOML file."`
}

// Load reads and parses the migration state file at path.
func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, errors.Wrap(err, "read migration state")
	}
	return errors.WithStack2(Parse(data))
}

// Parse reads and checks a migration state. An invalid state returns an
// *InvalidError.
func Parse(data []byte) (State, error) {
	var decoded file
	err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&decoded)
	if strict, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		problems := make([]Problem, 0, len(strict.Errors))
		for _, unknown := range strict.Errors {
			line, _ := unknown.Position()
			problems = append(problems, Problem{Line: line, Message: keyPath(unknown.Key()...) + ": unknown key"})
		}
		return State{}, &InvalidError{Problems: problems}
	} else if err != nil {
		return State{}, &InvalidError{Problems: []Problem{decodeProblem(err)}}
	}
	lines := keyLines(data)
	state := State{}
	for _, id := range slices.Sorted(maps.Keys(decoded.Nodes)) {
		node := decoded.Nodes[id]
		node.ID = id
		node.Line = lines[keyPath(nodesKey, id)]
		if node.Status == "" {
			node.Status = Pending
		}
		state.Nodes = append(state.Nodes, node)
	}
	if err := newChecker(lines).check(state); err != nil {
		return State{}, err
	}
	return state, nil
}

// decodeProblem moves the position of a go-toml error into the problem's line,
// and names the key it was decoding, if any.
func decodeProblem(err error) Problem {
	decodeErr, ok := errors.AsType[*toml.DecodeError](err)
	if !ok {
		return Problem{Message: err.Error()}
	}
	line, _ := decodeErr.Position()
	message := strings.TrimPrefix(decodeErr.Error(), "toml: ")
	if key := decodeErr.Key(); len(key) > 0 {
		message = keyPath(key...) + ": " + message
	}
	return Problem{Line: line, Message: message}
}

// encode writes nodes as a state file, with keys sorted.
func encode(nodes []Node) ([]byte, error) {
	encoded := file{Nodes: make(map[string]Node, len(nodes))}
	for _, node := range nodes {
		encoded.Nodes[node.ID] = node
	}
	data, err := toml.Marshal(encoded)
	return data, errors.WithStack(err)
}

// keyLines maps the key path of each table and key in a TOML document to the
// line that first defines it. The document must already have decoded.
func keyLines(data []byte) map[string]int {
	lines := map[string]int{}
	parser := unstable.Parser{}
	parser.Reset(data)
	var table []string
	for parser.NextExpression() {
		expression := parser.Expression()
		// Without KeepComments, the parser yields only key-values and headers.
		if expression.Kind == unstable.KeyValue {
			recordKeyValue(&parser, lines, table, expression)
		} else {
			table = recordKey(&parser, lines, nil, expression.Key())
		}
	}
	return lines
}

// recordKeyValue records the lines of a key and of the keys of its value, if
// that is an inline table.
func recordKeyValue(parser *unstable.Parser, lines map[string]int, table []string, keyValue *unstable.Node) {
	path := recordKey(parser, lines, table, keyValue.Key())
	value := keyValue.Value()
	if value.Kind != unstable.InlineTable {
		return
	}
	for children := value.Children(); children.Next(); {
		recordKeyValue(parser, lines, path, children.Node())
	}
}

// recordKey records the line of each part of a dotted key below prefix that
// has none yet, and returns the key's full path.
func recordKey(parser *unstable.Parser, lines map[string]int, prefix []string, key unstable.Iterator) []string {
	path := slices.Clone(prefix)
	for key.Next() {
		part := key.Node()
		path = append(path, string(part.Data))
		if _, ok := lines[keyPath(path...)]; !ok {
			lines[keyPath(path...)] = parser.Shape(part.Raw).Start.Line
		}
	}
	return path
}

// Unreached returns a warning for each node that no ingress node is or depends
// on, since ingress traffic never reaches its code.
func (s State) Unreached() []Problem {
	nodes := make(map[string]Node, len(s.Nodes))
	var queue []string
	for _, node := range s.Nodes {
		nodes[node.ID] = node
		if node.hasIngress() {
			queue = append(queue, node.ID)
		}
	}
	reached := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if reached[id] {
			continue
		}
		reached[id] = true
		queue = append(queue, nodes[id].DependsOn...)
	}
	var warnings []Problem
	for _, node := range s.Nodes {
		if !reached[node.ID] {
			warnings = append(warnings, Problem{Line: node.Line, Message: keyPath(nodesKey, node.ID) +
				": no ingress node reaches it, so SPECTRE cannot verify it"})
		}
	}
	return warnings
}

// Next returns the smallest set of pending nodes whose migration ingress traffic
// can verify: one ingress node and the pending nodes it depends on.
func (s State) Next() []Node {
	nodes := make(map[string]Node, len(s.Nodes))
	for _, node := range s.Nodes {
		nodes[node.ID] = node
	}
	var next []Node
	nextIO := 0
	for _, node := range s.Nodes {
		if node.Status != Pending || !node.hasIngress() {
			continue
		}
		step := pendingStep(nodes, node.ID)
		io := 0
		for _, member := range step {
			if len(member.kinds()) > 0 {
				io++
			}
		}
		// Fewer nodes with IO first, so a step holds one where it can.
		if len(next) == 0 || io < nextIO || (io == nextIO && len(step) < len(next)) {
			next, nextIO = step, io
		}
	}
	return next
}

// Pending returns the nodes that are not migrated yet.
func (s State) Pending() []Node {
	var pending []Node
	for _, node := range s.Nodes {
		if node.Status == Pending {
			pending = append(pending, node)
		}
	}
	return pending
}

// pendingStep returns id and the pending nodes it depends on, directly or not,
// dependencies first and otherwise by ID.
func pendingStep(nodes map[string]Node, id string) []Node {
	members := map[string]bool{}
	queue := []string{id}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if members[current] || nodes[current].Status != Pending {
			continue
		}
		members[current] = true
		queue = append(queue, nodes[current].DependsOn...)
	}
	ids := slices.Sorted(maps.Keys(members))
	step := make([]Node, 0, len(ids))
	placed := map[string]bool{}
	for len(step) < len(ids) {
		for _, member := range ids {
			ready := !placed[member] && !slices.ContainsFunc(nodes[member].DependsOn, func(dependency string) bool {
				return members[dependency] && !placed[dependency]
			})
			if ready {
				placed[member] = true
				step = append(step, nodes[member])
				break
			}
		}
	}
	return step
}

// Types returns the node's types: library, or each of its IO kinds.
func (n Node) Types() []string {
	kinds := n.kinds()
	if len(kinds) == 0 {
		return []string{Library}
	}
	types := make([]string, len(kinds))
	for i, kind := range kinds {
		types[i] = kind.String()
	}
	return types
}

// kinds returns the node's IO kinds, by protocol with ingress first.
func (n Node) kinds() []model.IOKind {
	var kinds []model.IOKind
	if n.Ingress.HTTPSettings().Ok() {
		kinds = append(kinds, model.IOKind{Direction: model.Ingress, Protocol: model.ProtocolHTTP})
	}
	if n.Egress.HTTPSettings().Ok() {
		kinds = append(kinds, model.IOKind{Direction: model.Egress, Protocol: model.ProtocolHTTP})
	}
	return kinds
}

func (n Node) hasIngress() bool {
	return n.Ingress != Ingress{}
}

// checker collects every problem with a decoded state instead of stopping at the first.
type checker struct {
	lines    map[string]int
	problems []Problem
}

func newChecker(lines map[string]int) *checker {
	return &checker{lines: lines}
}

func (c *checker) check(state State) error {
	for _, node := range state.Nodes {
		c.checkNode(node)
	}
	c.checkDependencies(state)
	c.checkRoutes(state)
	if len(c.problems) == 0 {
		return nil
	}
	slices.SortStableFunc(c.problems, func(a, b Problem) int { return a.Line - b.Line })
	return &InvalidError{Problems: c.problems}
}

func (c *checker) reportf(path []string, format string, args ...any) {
	c.problems = append(c.problems, Problem{Line: c.lines[keyPath(path...)], Message: keyPath(path...) + ": " + fmt.Sprintf(format, args...)})
}

func (c *checker) checkNode(node Node) {
	if !nodeIDPattern.MatchString(node.ID) {
		c.reportf([]string{nodesKey, node.ID}, "node IDs must be kebab-case")
	}
	if node.Status != Pending && node.Status != Migrated {
		c.reportf([]string{nodesKey, node.ID, statusKey}, "must be %q or %q", Pending, Migrated)
	}
	if http, ok := node.Ingress.HTTPSettings().Get(); ok && len(http.Routes) == 0 {
		c.reportf([]string{nodesKey, node.ID, string(model.Ingress), string(model.ProtocolHTTP)}, "routes must list at least one route")
	}
}

// checkDependencies reports dependencies on unknown nodes, cycles, and migrated
// nodes that depend on pending ones.
func (c *checker) checkDependencies(state State) {
	nodes := make(map[string]Node, len(state.Nodes))
	for _, node := range state.Nodes {
		nodes[node.ID] = node
	}
	for _, node := range state.Nodes {
		path := []string{nodesKey, node.ID, dependsOnKey}
		seen := map[string]bool{}
		for _, id := range node.DependsOn {
			dependency, ok := nodes[id]
			switch {
			case id == node.ID:
				c.reportf(path, "a node cannot depend on itself")
			case !ok:
				c.reportf(path, "%q is not a node", id)
			case seen[id]:
				c.reportf(path, "lists %q twice", id)
			default:
				seen[id] = true
				if node.Status == Migrated && dependency.Status == Pending {
					c.reportf([]string{nodesKey, node.ID, statusKey}, "migrated, but depends on pending node %q", id)
				}
			}
		}
	}
	c.checkCycles(state, nodes)
}

// checkCycles reports the first dependency cycle it finds, starting from the
// lowest node ID.
func (c *checker) checkCycles(state State, nodes map[string]Node) {
	done := map[string]bool{}
	var visit func(stack []string) bool
	visit = func(stack []string) bool {
		id := stack[len(stack)-1]
		if done[id] {
			return false
		}
		for _, dependency := range nodes[id].DependsOn {
			if dependency == id {
				continue
			}
			if start := slices.Index(stack, dependency); start >= 0 {
				cycle := append(slices.Clone(stack[start:]), dependency)
				c.reportf([]string{nodesKey, dependency, dependsOnKey}, "dependency cycle %s", strings.Join(cycle, " -> "))
				return true
			}
			if visit(append(stack, dependency)) {
				return true
			}
		}
		done[id] = true
		return false
	}
	for _, node := range state.Nodes {
		if visit([]string{node.ID}) {
			return
		}
	}
}

// checkRoutes reports an ingress[http] route that more than one node lists.
func (c *checker) checkRoutes(state State) {
	owners := map[model.Route]string{}
	for _, node := range state.Nodes {
		http, ok := node.Ingress.HTTPSettings().Get()
		if !ok {
			continue
		}
		for _, route := range slices.Sorted(maps.Keys(http.Routes)) {
			if owner, listed := owners[route]; listed {
				path := []string{nodesKey, node.ID, string(model.Ingress), string(model.ProtocolHTTP), "routes", string(route)}
				c.reportf(path, "also listed by node %q", owner)
				continue
			}
			owners[route] = node.ID
		}
	}
}

var bareKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// keyPath writes a TOML dotted key, quoting parts that are not bare keys.
func keyPath(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = part
		if !bareKeyPattern.MatchString(part) {
			quoted[i] = strconv.Quote(part)
		}
	}
	return strings.Join(quoted, ".")
}

package state

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/errors"
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
	ID        string
	DependsOn []string
	Status    Status
	// IO holds the settings of each IO kind the node has. A library has none.
	IO map[model.IOKind]map[string]any
	// Line is where the node's table starts in the file, or 0 when unknown.
	Line int
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

// syntaxProblem moves the position of a TOML syntax error into the problem's
// line.
func syntaxProblem(err error) Problem {
	decodeErr, ok := errors.AsType[*toml.DecodeError](err)
	if !ok {
		return Problem{Message: err.Error()}
	}
	line, _ := decodeErr.Position()
	return Problem{Line: line, Message: strings.TrimPrefix(decodeErr.Error(), "toml: ")}
}

// Keys of the state file that the parser recognises.
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
func Load(path string, catalog *Catalog) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, errors.Wrap(err, "read migration state")
	}
	return errors.WithStack2(Parse(data, catalog))
}

// Parse reads a migration state and checks it against the IO kinds the catalog
// supports. An invalid state returns an *InvalidError.
func Parse(data []byte, catalog *Catalog) (State, error) {
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return State{}, &InvalidError{Problems: []Problem{syntaxProblem(err)}}
	}
	return newParser(root, keyLines(data), catalog).parse()
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

func (n Node) hasIngress() bool {
	for kind := range n.IO {
		if kind.Direction == model.Ingress {
			return true
		}
	}
	return false
}

// parser collects every problem in a state file instead of stopping at the first.
type parser struct {
	root     map[string]any
	lines    map[string]int
	catalog  *Catalog
	problems []Problem
}

func newParser(root map[string]any, lines map[string]int, catalog *Catalog) *parser {
	return &parser{root: root, lines: lines, catalog: catalog}
}

func (p *parser) parse() (State, error) {
	state := p.state()
	p.checkDependencies(state)
	p.checkUnique(state)
	if len(p.problems) > 0 {
		slices.SortStableFunc(p.problems, func(a, b Problem) int { return a.Line - b.Line })
		return State{}, &InvalidError{Problems: p.problems}
	}
	return state, nil
}

func (p *parser) reportf(path []string, format string, args ...any) {
	p.problems = append(p.problems, Problem{Line: p.line(path), Message: keyPath(path...) + ": " + fmt.Sprintf(format, args...)})
}

// line returns where path is defined, or 0 when unknown.
func (p *parser) line(path []string) int {
	return p.lines[keyPath(path...)]
}

func (p *parser) state() State {
	for _, key := range sortedKeys(p.root) {
		if key != nodesKey {
			p.reportf([]string{key}, "unknown key; the state holds only nodes")
		}
	}
	value, ok := p.root[nodesKey]
	if !ok {
		return State{}
	}
	nodes, ok := value.(map[string]any)
	if !ok {
		p.reportf([]string{nodesKey}, "must be a table of nodes keyed by ID")
		return State{}
	}
	state := State{}
	for _, id := range sortedKeys(nodes) {
		path := []string{nodesKey, id}
		if !nodeIDPattern.MatchString(id) {
			p.reportf(path, "node IDs must be kebab-case")
		}
		table, isTable := nodes[id].(map[string]any)
		if !isTable {
			p.reportf(path, "must be a table")
			state.Nodes = append(state.Nodes, Node{ID: id, Status: Pending, Line: p.line(path)})
			continue
		}
		state.Nodes = append(state.Nodes, p.node(path, table))
	}
	return state
}

func (p *parser) node(path []string, table map[string]any) Node {
	node := Node{ID: path[len(path)-1], Status: Pending, IO: map[model.IOKind]map[string]any{}, Line: p.line(path)}
	for _, key := range sortedKeys(table) {
		keyPath := append(slices.Clone(path), key)
		value := table[key]
		switch key {
		case dependsOnKey:
			node.DependsOn = p.dependsOn(keyPath, value)
		case statusKey:
			status, ok := value.(string)
			if !ok || (Status(status) != Pending && Status(status) != Migrated) {
				p.reportf(keyPath, "must be %q or %q", Pending, Migrated)
				continue
			}
			node.Status = Status(status)
		case string(model.Ingress), string(model.Egress):
			p.kinds(keyPath, model.Direction(key), value, node.IO)
		default:
			p.reportf(keyPath, "unknown key; a node holds depends_on, status, %s, and %s", model.Ingress, model.Egress)
		}
	}
	return node
}

func (p *parser) dependsOn(path []string, value any) []string {
	values, ok := value.([]any)
	if !ok {
		p.reportf(path, "must be an array of node IDs")
		return nil
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id, isString := value.(string)
		if !isString {
			p.reportf(path, "must be an array of node IDs")
			return nil
		}
		ids = append(ids, id)
	}
	return ids
}

func (p *parser) kinds(path []string, direction model.Direction, value any, io map[model.IOKind]map[string]any) {
	protocols, ok := value.(map[string]any)
	if !ok {
		p.reportf(path, "must be a table keyed by protocol")
		return
	}
	for _, protocol := range sortedKeys(protocols) {
		kindPath := append(slices.Clone(path), protocol)
		kind := model.IOKind{Direction: direction, Protocol: model.Protocol(protocol)}
		support, supported := p.catalog.Support(kind)
		if !supported {
			p.reportf(kindPath, "%s is not a supported IO kind; run spectre state types for the supported ones", kind)
			continue
		}
		settings, isTable := protocols[protocol].(map[string]any)
		if !isTable {
			p.reportf(kindPath, "must be a table of %s settings", kind)
			continue
		}
		p.settings(kindPath, settings, support.Settings)
		io[kind] = settings
	}
}

// settings checks a table against the settings it may hold, and each entry of a
// keyed setting against the settings an entry may hold.
func (p *parser) settings(path []string, table map[string]any, settings []Setting) {
	for _, setting := range settings {
		if _, ok := table[setting.Name]; setting.Required && !ok {
			p.reportf(path, "%s is required", setting.Name)
		}
	}
	for _, key := range sortedKeys(table) {
		settingPath := append(slices.Clone(path), key)
		index := slices.IndexFunc(settings, func(setting Setting) bool { return setting.Name == key })
		if index < 0 {
			p.reportf(settingPath, "unknown setting")
			continue
		}
		setting := settings[index]
		value := table[key]
		if setting.Key == "" {
			if _, ok := value.(map[string]any); ok {
				p.reportf(settingPath, "must be a value, not a table")
			}
			continue
		}
		entries, ok := value.(map[string]any)
		if !ok {
			p.reportf(settingPath, "must be a table keyed by %s", setting.Key)
			continue
		}
		for _, entryKey := range sortedKeys(entries) {
			entryPath := append(slices.Clone(settingPath), entryKey)
			entry, isTable := entries[entryKey].(map[string]any)
			if !isTable {
				p.reportf(entryPath, "must be a table; write {} for an entry with no settings")
				continue
			}
			p.settings(entryPath, entry, setting.Entry)
		}
	}
}

// checkDependencies reports dependencies on unknown nodes, cycles, and migrated
// nodes that depend on pending ones.
func (p *parser) checkDependencies(state State) {
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
				p.reportf(path, "a node cannot depend on itself")
			case !ok:
				p.reportf(path, "%q is not a node", id)
			case seen[id]:
				p.reportf(path, "lists %q twice", id)
			default:
				seen[id] = true
				if node.Status == Migrated && dependency.Status == Pending {
					p.reportf([]string{nodesKey, node.ID, statusKey}, "migrated, but depends on pending node %q", id)
				}
			}
		}
	}
	p.checkCycles(state, nodes)
}

// checkCycles reports the first dependency cycle it finds, starting from the
// lowest node ID.
func (p *parser) checkCycles(state State, nodes map[string]Node) {
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
				p.reportf([]string{nodesKey, dependency, dependsOnKey}, "dependency cycle %s", strings.Join(cycle, " -> "))
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

// checkUnique reports a key of a unique setting that more than one node lists.
func (p *parser) checkUnique(state State) {
	for _, support := range p.catalog.Supports() {
		for _, setting := range support.Settings {
			if !setting.Unique {
				continue
			}
			owners := map[string]string{}
			for _, node := range state.Nodes {
				entries, _ := node.IO[support.Kind][setting.Name].(map[string]any)
				for _, key := range slices.Sorted(maps.Keys(entries)) {
					path := []string{nodesKey, node.ID, string(support.Kind.Direction), string(support.Kind.Protocol), setting.Name, key}
					if owner, ok := owners[key]; ok {
						p.reportf(path, "also listed by node %q", owner)
						continue
					}
					owners[key] = node.ID
				}
			}
		}
	}
}

func sortedKeys(table map[string]any) []string {
	return slices.Sorted(maps.Keys(table))
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

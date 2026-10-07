package main

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	. "github.com/alecthomas/types/optional"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/edge"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/types/typeutil"
)

// allowDirective suppresses a report on its own line or the line below.
const allowDirective = "//optionalnil:allow"

// newOptionalNil returns an analyzer that reports nil used as an absent value in
// code belonging to the analysed package's module.
func newOptionalNil() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "optionalnil",
		Doc: "report nil used as an absent value instead of Option\n\n" +
			"Use " + allowDirective + " <reason> to keep an intentional nil.",
		Requires:  []*analysis.Analyzer{inspect.Analyzer},
		FactTypes: []analysis.Fact{new(generatedPackage)},
		Run:       run,
	}
}

// generatedPackage marks a package whose files are all generated. Its API follows
// the generator's conventions, so nil uses of it are not reported.
type generatedPackage struct{}

func (*generatedPackage) AFact() {}

func (*generatedPackage) String() string { return "generatedPackage" }

func run(pass *analysis.Pass) (any, error) {
	analyse(pass)
	return nil, nil //nolint:nilnil // The analyzer only reports diagnostics.
}

func analyse(pass *analysis.Pass) {
	if pass.Module == nil || !inModule(pass.Pkg.Path(), pass.Module.Path) {
		return // Neither dependencies nor test mains are first-party.
	}
	if allGenerated(pass.Files) {
		pass.ExportPackageFact(&generatedPackage{})
		return
	}
	root := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector).Root()
	c := newChecker(pass, root)
	for cur := range root.Preorder((*ast.Ident)(nil)) {
		if pass.TypesInfo.Types[cur.Node().(*ast.Ident)].IsNil() {
			c.checkNil(cur)
		}
	}
}

type checker struct {
	pass      *analysis.Pass
	module    string
	errorType *types.Interface
	allowed   map[string]map[int]bool
	tracked   map[types.Object]bool
}

func newChecker(pass *analysis.Pass, root inspector.Cursor) *checker {
	c := &checker{
		pass:      pass,
		module:    pass.Module.Path,
		errorType: types.Universe.Lookup("error").Type().Underlying().(*types.Interface),
		allowed:   allowedLines(pass),
		tracked:   map[types.Object]bool{},
	}
	c.trackLocals(root)
	return c
}

// checkNil reports a nil literal whose destination is a first-party slot for a
// value that may be absent.
func (c *checker) checkNil(cur inspector.Cursor) {
	for cur.ParentEdgeKind() == edge.ParenExpr_X {
		cur = cur.Parent()
	}
	kind, index := cur.ParentEdge()
	parent := cur.Parent()
	switch kind { //nolint:exhaustive // Only edges that give the nil a destination matter.
	case edge.ReturnStmt_Results:
		c.checkReturn(parent, index)
	case edge.AssignStmt_Rhs:
		assign := parent.Node().(*ast.AssignStmt)
		if len(assign.Lhs) == len(assign.Rhs) {
			c.checkStore(cur, c.referencedVar(assign.Lhs[index]))
		}
	case edge.ValueSpec_Values:
		spec := parent.Node().(*ast.ValueSpec)
		if len(spec.Names) == len(spec.Values) {
			c.checkStore(cur, c.referencedVar(spec.Names[index]))
		}
	case edge.KeyValueExpr_Value:
		key := parent.Node().(*ast.KeyValueExpr).Key
		if parent.ParentEdgeKind() == edge.CompositeLit_Elts && c.isStruct(parent.Parent()) {
			c.checkStore(cur, c.referencedVar(key))
		}
	case edge.CompositeLit_Elts:
		if structType, ok := c.pass.TypesInfo.TypeOf(parent.Node().(*ast.CompositeLit)).Underlying().(*types.Struct); ok {
			c.checkStore(cur, Some(structType.Field(index)))
		}
	case edge.CallExpr_Args:
		c.checkArgument(cur, parent.Node().(*ast.CallExpr), index)
	case edge.BinaryExpr_X, edge.BinaryExpr_Y:
		c.checkComparison(parent, kind)
	}
}

func (c *checker) checkReturn(ret inspector.Cursor, index int) {
	stmt := ret.Node().(*ast.ReturnStmt)
	signature, ok := c.enclosingSignature(ret)
	if !ok || signature.Results().Len() != len(stmt.Results) || c.isPlaceholder(stmt, index) {
		return
	}
	if c.mayBeAbsent(signature.Results().At(index).Type(), false) {
		c.report(stmt.Results[index], "nil returned")
	}
}

// isPlaceholder reports whether another result carries the outcome, such as an
// error or a false ok flag, so the nil only fills its slot.
func (c *checker) isPlaceholder(stmt *ast.ReturnStmt, index int) bool {
	for i, result := range stmt.Results {
		value := c.pass.TypesInfo.Types[result]
		if i == index || value.IsNil() {
			continue
		}
		if value.Value == nil || (value.Value.Kind() == constant.Bool && !constant.BoolVal(value.Value)) {
			return true
		}
	}
	return false
}

func (c *checker) checkStore(cur inspector.Cursor, target Option[*types.Var]) {
	variable, ok := target.Get()
	if ok && c.isFirstParty(variable) && c.mayBeAbsent(variable.Type(), false) {
		c.report(cur.Node(), "nil stored in "+variable.Name())
	}
}

func (c *checker) checkArgument(cur inspector.Cursor, call *ast.CallExpr, index int) {
	callee := typeutil.Callee(c.pass.TypesInfo, call)
	signature, ok := c.pass.TypesInfo.TypeOf(call.Fun).Underlying().(*types.Signature)
	if callee == nil || !ok || !c.isFirstParty(callee) || c.inTestFile(call) {
		return
	}
	params := signature.Params()
	var paramType types.Type
	switch {
	case !signature.Variadic() || index < params.Len()-1:
		paramType = params.At(index).Type()
	case call.Ellipsis == token.NoPos:
		paramType = params.At(params.Len() - 1).Type().(*types.Slice).Elem()
	default:
		return
	}
	if c.mayBeAbsent(paramType, false) {
		c.report(cur.Node(), "nil passed to "+callee.Name())
	}
}

// checkComparison reports a nil comparison that tests whether a first-party value
// is present. Slices and maps count since nil there separates absent from empty.
func (c *checker) checkComparison(cur inspector.Cursor, nilSide edge.Kind) {
	comparison := cur.Node().(*ast.BinaryExpr)
	if comparison.Op != token.EQL && comparison.Op != token.NEQ {
		return
	}
	operand := comparison.X
	if nilSide == edge.BinaryExpr_X {
		operand = comparison.Y
	}
	object, ok := c.referencedObject(operand)
	if !ok || !c.isFirstParty(object) || !c.mayBeAbsent(c.pass.TypesInfo.TypeOf(operand), true) {
		return
	}
	if isLocal(object) && !c.tracked[object] {
		return // Its nil came from code outside the module.
	}
	if comparison.Op == token.EQL && c.isRequirementCheck(cur) {
		return
	}
	c.report(comparison, "nil compared with "+object.Name())
}

// isRequirementCheck reports whether a nil comparison fails when the value is
// missing, which treats the value as required rather than optional.
func (c *checker) isRequirementCheck(cur inspector.Cursor) bool {
	for cur.ParentEdgeKind() == edge.ParenExpr_X ||
		(cur.ParentEdgeKind() == edge.BinaryExpr_X || cur.ParentEdgeKind() == edge.BinaryExpr_Y) &&
			cur.Parent().Node().(*ast.BinaryExpr).Op == token.LOR {
		cur = cur.Parent()
	}
	if cur.ParentEdgeKind() != edge.IfStmt_Cond {
		return false
	}
	body := cur.Parent().Node().(*ast.IfStmt).Body.List
	return len(body) > 0 && c.fails(body[len(body)-1])
}

// fails reports whether a statement panics or returns a non-nil error.
func (c *checker) fails(stmt ast.Stmt) bool {
	switch stmt := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := stmt.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		builtin, ok := typeutil.Callee(c.pass.TypesInfo, call).(*types.Builtin)
		return ok && builtin.Name() == "panic"
	case *ast.ReturnStmt:
		for _, result := range stmt.Results {
			value := c.pass.TypesInfo.Types[result]
			if types.Implements(value.Type, c.errorType) && !value.IsNil() {
				return true
			}
		}
	}
	return false
}

// mayBeAbsent reports whether nil can mean absent for a type. It cannot for errors,
// self-referential pointers ending a linked structure, or any holding JSON null.
func (c *checker) mayBeAbsent(valueType types.Type, includeCollections bool) bool {
	if _, ok := types.Unalias(valueType).(*types.TypeParam); ok || types.Implements(valueType, c.errorType) {
		return false
	}
	switch underlying := valueType.Underlying().(type) {
	case *types.Pointer:
		return !isSelfReferential(valueType, underlying)
	case *types.Interface:
		return !underlying.Empty()
	case *types.Signature, *types.Chan:
		return true
	case *types.Slice, *types.Map:
		return includeCollections
	}
	return false
}

func isSelfReferential(pointer types.Type, underlying *types.Pointer) bool {
	structType, ok := underlying.Elem().Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for field := range structType.Fields() {
		if types.Identical(field.Type(), pointer) {
			return true
		}
	}
	return false
}

// isFirstParty reports whether an object belongs to the analysed module and was
// written by hand.
func (c *checker) isFirstParty(object types.Object) bool {
	pkg := object.Pkg()
	if pkg == nil || !inModule(pkg.Path(), c.module) {
		return false
	}
	return !c.pass.ImportPackageFact(pkg, new(generatedPackage))
}

func (c *checker) isStruct(lit inspector.Cursor) bool {
	_, ok := c.pass.TypesInfo.TypeOf(lit.Node().(*ast.CompositeLit)).Underlying().(*types.Struct)
	return ok
}

func (c *checker) referencedVar(expr ast.Expr) Option[*types.Var] {
	object, ok := c.referencedObject(expr)
	if !ok {
		return None[*types.Var]()
	}
	variable, ok := object.(*types.Var)
	if !ok {
		return None[*types.Var]()
	}
	return Some(variable)
}

// referencedObject returns the variable, field, or called function an expression
// names.
func (c *checker) referencedObject(expr ast.Expr) (object types.Object, ok bool) {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.Ident:
		object = c.pass.TypesInfo.ObjectOf(expr)
	case *ast.SelectorExpr:
		object = c.pass.TypesInfo.ObjectOf(expr.Sel)
	case *ast.CallExpr:
		object = typeutil.Callee(c.pass.TypesInfo, expr)
	}
	switch object.(type) {
	case *types.Var, *types.Func:
		return object, true
	}
	return nil, false
}

func (c *checker) enclosingSignature(cur inspector.Cursor) (signature *types.Signature, ok bool) {
	for function := range cur.Enclosing((*ast.FuncDecl)(nil), (*ast.FuncLit)(nil)) {
		switch function := function.Node().(type) {
		case *ast.FuncDecl:
			signature, ok = c.pass.TypesInfo.Defs[function.Name].Type().(*types.Signature)
		case *ast.FuncLit:
			signature, ok = c.pass.TypesInfo.TypeOf(function).(*types.Signature)
		}
		return signature, ok
	}
	return nil, false
}

func inModule(pkgPath, module string) bool {
	return pkgPath == module || strings.HasPrefix(pkgPath, module+"/")
}

// isLocal reports whether an object is a variable declared inside a function,
// excluding parameters and results.
func isLocal(object types.Object) bool {
	variable, ok := object.(*types.Var)
	return ok && variable.Kind() == types.LocalVar
}

func (c *checker) inTestFile(node ast.Node) bool {
	return strings.HasSuffix(c.pass.Fset.Position(node.Pos()).Filename, "_test.go")
}

func (c *checker) report(node ast.Node, what string) {
	position := c.pass.Fset.Position(node.Pos())
	lines := c.allowed[position.Filename]
	if lines[position.Line] || lines[position.Line-1] {
		return
	}
	c.pass.Reportf(node.Pos(), "%s; use Option for a value that may be absent", what)
}

func allowedLines(pass *analysis.Pass) map[string]map[int]bool {
	allowed := map[string]map[int]bool{}
	for _, file := range pass.Files {
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if !strings.HasPrefix(comment.Text, allowDirective) {
					continue
				}
				position := pass.Fset.Position(comment.Pos())
				if allowed[position.Filename] == nil {
					allowed[position.Filename] = map[int]bool{}
				}
				allowed[position.Filename][position.Line] = true
			}
		}
	}
	return allowed
}

// trackLocals records the locals whose nil originates in the module: those
// declared without a value and those set from a first-party call.
func (c *checker) trackLocals(root inspector.Cursor) {
	for cur := range root.Preorder((*ast.ValueSpec)(nil), (*ast.AssignStmt)(nil)) {
		var names []ast.Expr
		var values []ast.Expr
		switch node := cur.Node().(type) {
		case *ast.ValueSpec:
			for _, name := range node.Names {
				names = append(names, name)
			}
			values = node.Values
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE {
				continue
			}
			names, values = node.Lhs, node.Rhs
		}
		if len(values) > 0 && !c.isFirstPartyCall(values) {
			continue
		}
		for _, name := range names {
			if object := c.pass.TypesInfo.Defs[name.(*ast.Ident)]; object != nil && isLocal(object) {
				c.tracked[object] = true
			}
		}
	}
}

func (c *checker) isFirstPartyCall(values []ast.Expr) bool {
	if len(values) != 1 {
		return false
	}
	call, ok := ast.Unparen(values[0]).(*ast.CallExpr)
	if !ok {
		return false
	}
	callee := typeutil.Callee(c.pass.TypesInfo, call)
	return callee != nil && c.isFirstParty(callee)
}

func allGenerated(files []*ast.File) bool {
	for _, file := range files {
		if !ast.IsGenerated(file) {
			return false
		}
	}
	return len(files) > 0
}

package main

import (
	"go/ast"
	"go/types"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/analysis"
)

func newAnalyzer() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "encapsulation",
		Doc:  "require encapsulated structs to expose fields through methods and construction through constructors",
		Run:  run,
	}
}

func run(pass *analysis.Pass) (any, error) {
	// Fields are keyed by go/types identity so promoted and generic uses resolve correctly.
	fieldOwners := map[*types.Var]*types.Named{}
	for _, object := range pass.TypesInfo.Defs {
		typeName, ok := object.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := namedType(typeName.Type())
		if !ok {
			continue
		}
		structure, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for field := range structure.Fields() {
			fieldOwners[field] = named.Origin()
		}
	}

	for _, file := range pass.Files {
		if ast.IsGenerated(file) {
			continue
		}
		ast.Walk(newVisitor(pass, fieldOwners, nil), file)
	}
	// An analyzer without ResultType must return a nil result.
	return nil, nil //nolint:nilnil
}

type visitor struct {
	pass        *analysis.Pass
	fieldOwners map[*types.Var]*types.Named
	function    *ast.FuncDecl
}

func newVisitor(pass *analysis.Pass, fieldOwners map[*types.Var]*types.Named, function *ast.FuncDecl) *visitor {
	return &visitor{pass: pass, fieldOwners: fieldOwners, function: function}
}

func (v *visitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	if function, ok := node.(*ast.FuncDecl); ok {
		return newVisitor(v.pass, v.fieldOwners, function)
	}

	switch expression := node.(type) {
	case *ast.SelectorExpr:
		v.checkFieldAccess(expression)
	case *ast.CompositeLit:
		v.checkConstruction(expression, v.pass.TypesInfo.TypeOf(expression))
	case *ast.CallExpr:
		v.checkNewCall(expression)
	}
	return v
}

func (v *visitor) checkFieldAccess(expression *ast.SelectorExpr) {
	selection := v.pass.TypesInfo.Selections[expression]
	if selection == nil || selection.Kind() != types.FieldVal {
		return
	}
	field, ok := selection.Obj().(*types.Var)
	if !ok || field.Exported() {
		return
	}
	owner, ok := v.fieldOwners[field]
	if !ok || !requiresEncapsulation(owner) || sameNamedType(owner, receiverType(v.pass, v.function)) || isConstructor(v.pass, v.function, owner) {
		return
	}
	v.pass.Reportf(expression.Sel.Pos(), "private field %s may only be accessed by methods of %s", field.Name(), owner.Obj().Name())
}

func (v *visitor) checkConstruction(node ast.Node, objectType types.Type) {
	named, ok := encapsulatedType(v.pass, objectType)
	if !ok || isConstructor(v.pass, v.function, named) {
		return
	}
	suffix := constructorSuffix(named.Obj().Name())
	v.pass.Reportf(
		node.Pos(),
		"%s may only be constructed by New, %sf, or a constructor ending in %s",
		named.Obj().Name(),
		named.Obj().Name(),
		suffix,
	)
}

func (v *visitor) checkNewCall(call *ast.CallExpr) {
	identifier, ok := call.Fun.(*ast.Ident)
	if !ok || len(call.Args) != 1 {
		return
	}
	builtin, ok := v.pass.TypesInfo.Uses[identifier].(*types.Builtin)
	if !ok || builtin.Name() != "new" {
		return
	}
	v.checkConstruction(call, v.pass.TypesInfo.TypeOf(call.Args[0]))
}

func encapsulatedType(pass *analysis.Pass, objectType types.Type) (*types.Named, bool) {
	named, ok := namedType(objectType)
	if !ok || named.Obj().Pkg() != pass.Pkg || !requiresEncapsulation(named) {
		return nil, false
	}
	structure, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, false
	}
	for field := range structure.Fields() {
		if !field.Exported() {
			return named.Origin(), true
		}
	}
	return nil, false
}

func receiverType(pass *analysis.Pass, function *ast.FuncDecl) *types.Named {
	if function == nil || function.Recv == nil || len(function.Recv.List) == 0 {
		return nil
	}
	named, _ := namedType(pass.TypesInfo.TypeOf(function.Recv.List[0].Type))
	return named
}

func namedType(objectType types.Type) (*types.Named, bool) {
	objectType = types.Unalias(objectType)
	if pointer, ok := objectType.(*types.Pointer); ok {
		objectType = types.Unalias(pointer.Elem())
	}
	named, ok := objectType.(*types.Named)
	return named, ok
}

func sameNamedType(left *types.Named, right *types.Named) bool {
	return left != nil && right != nil && left.Origin() == right.Origin()
}

func requiresEncapsulation(named *types.Named) bool {
	named = named.Origin()
	return named.Obj().Exported() || named.NumMethods() > 0
}

func isConstructor(pass *analysis.Pass, function *ast.FuncDecl, named *types.Named) bool {
	if function == nil || function.Recv != nil {
		return false
	}
	suffix := constructorSuffix(named.Obj().Name())
	formatted := named.Obj().Name() + "f"
	if function.Name.Name != "New" && function.Name.Name != formatted && !strings.HasSuffix(function.Name.Name, suffix) {
		return false
	}
	object, ok := pass.TypesInfo.Defs[function.Name].(*types.Func)
	if !ok {
		return false
	}
	signature, ok := object.Type().(*types.Signature)
	if !ok {
		return false
	}
	for result := range signature.Results().Variables() {
		resultType, isNamed := namedType(result.Type())
		if isNamed && sameNamedType(named, resultType) {
			return true
		}
	}
	return false
}

func constructorSuffix(name string) string {
	first, size := utf8.DecodeRuneInString(name)
	return string(unicode.ToUpper(first)) + name[size:]
}

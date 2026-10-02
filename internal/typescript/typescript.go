// Package typescript re-exports the TypeScript 7 parser, checker, and transpiler.
// Its module path permits importing their internal packages.
package typescript

import (
	"fmt"
	"strings"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/checker"
	"github.com/microsoft/TypeScript/tsc/internal/scanner"
)

type (
	// Node is any syntax tree node.
	Node = ast.Node
	// SourceFile is the root of one parsed file.
	SourceFile = ast.SourceFile
	// Kind identifies the syntax of a node.
	Kind = ast.Kind
	// Symbol is a named entity the binder or checker created.
	Symbol = ast.Symbol
	// SymbolFlags classifies a symbol's declarations.
	SymbolFlags = ast.SymbolFlags
	// Checker answers type queries about one program.
	Checker = checker.Checker
	// Type is a resolved TypeScript type.
	Type = checker.Type
	// TypeFlags classifies a type.
	TypeFlags = checker.TypeFlags
	// Signature is one call signature of a function type.
	Signature = checker.Signature
)

// Syntax kinds Spectre inspects.
const (
	KindCallExpression    = ast.KindCallExpression
	KindModuleDeclaration = ast.KindModuleDeclaration
	KindStringLiteral     = ast.KindStringLiteral
)

// Symbol flags Spectre inspects.
const (
	SymbolFlagsAlias       = ast.SymbolFlagsAlias
	SymbolFlagsClass       = ast.SymbolFlagsClass
	SymbolFlagsInterface   = ast.SymbolFlagsInterface
	SymbolFlagsMethod      = ast.SymbolFlagsMethod
	SymbolFlagsModule      = ast.SymbolFlagsModule
	SymbolFlagsOptional    = ast.SymbolFlagsOptional
	SymbolFlagsTypeAlias   = ast.SymbolFlagsTypeAlias
	SymbolFlagsTypeLiteral = ast.SymbolFlagsTypeLiteral
	SymbolFlagsValue       = ast.SymbolFlagsValue
)

// Type flags Spectre inspects.
const (
	TypeFlagsBoolean       = checker.TypeFlagsBoolean
	TypeFlagsEnumLiteral   = checker.TypeFlagsEnumLiteral
	TypeFlagsNumber        = checker.TypeFlagsNumber
	TypeFlagsObject        = checker.TypeFlagsObject
	TypeFlagsString        = checker.TypeFlagsString
	TypeFlagsStringLiteral = checker.TypeFlagsStringLiteral
	TypeFlagsUnion         = checker.TypeFlagsUnion
)

// SignatureKindCall selects call signatures rather than construct signatures.
const SignatureKindCall = checker.SignatureKindCall

// SourceFileOf returns the file containing node.
func SourceFileOf(node *Node) *SourceFile {
	return ast.GetSourceFileOfNode(node)
}

// IsTupleType reports whether t is a tuple, which also satisfies array checks.
func IsTupleType(t *Type) bool {
	return checker.IsTupleType(t)
}

// StringLiteral returns the value of a string literal type.
func StringLiteral(t *Type) (value string, ok bool) {
	if t.Flags()&TypeFlagsStringLiteral == 0 {
		return "", false
	}
	value, ok = t.AsLiteralType().Value().(string)
	return value, ok
}

// Location formats a node's first token as file:line:column, with the file name
// relative to the program root.
func Location(node *Node) string {
	file := SourceFileOf(node)
	if file == nil {
		return "<unknown>"
	}
	line, column := scanner.GetECMALineAndUTF16CharacterOfPosition(file, scanner.GetTokenPosOfNode(node, file, false))
	return fmt.Sprintf("%s:%d:%d", strings.TrimPrefix(file.FileName(), "/"), line+1, int(column)+1)
}

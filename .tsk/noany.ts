import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as token from "go/token";
import * as types from "go/types";
import * as edge from "golang.org/x/tools/go/ast/edge";
import type { Cursor } from "golang.org/x/tools/go/ast/inspector";
import * as typeutil from "golang.org/x/tools/go/types/typeutil";

export default defineAnalyzer({
  name: "noany",
  doc: `report uses of any, interface{}, and json.RawMessage

An empty interface discards the type, so code that holds one needs type
assertions or reflection to use it. json.RawMessage likewise defers decoding to
untyped bytes. Use a concrete type, a type parameter, or a sum type instead, and
keep an unavoidable use with a nolint comment.

Uses that Go's type checker forces are allowed: an unconstrained type
parameter, as in [T any]; a method whose signature matches one of an interface
from an imported package that its type implements; a function whose signature
is exactly the function type another package's parameter or field expects; and
a final ...any parameter used only to forward its arguments to another
package's ...any parameter.`,
  requires: [inspect],
  run(pass) {
    const root = pass.resultOf(inspect).root();
    const universeAny = types.Universe?.lookup("any") ?? null;
    const forced = new Forced(pass.pkg, pass.typesInfo, root);
    const check = (cursor: Cursor, message: string) => {
      if (!isConstraint(cursor) && !forced.covers(cursor)) {
        pass.report({ pos: cursor.node()!.pos(), message });
      }
    };
    for (const cursor of root.preorder(ast.Ident)) {
      const ident = cursor.node() as ast.Ident;
      const object = pass.typesInfo.uses.get(ident);
      if (ident.name === "any" && object === universeAny) {
        check(cursor, emptyInterface);
      } else if (isRawMessage(object)) {
        check(cursor.parentEdgeKind() === edge.SelectorExpr_Sel ? cursor.parent() : cursor, rawMessage);
      }
    }
    for (const cursor of root.preorder(ast.InterfaceType)) {
      if (((cursor.node() as ast.InterfaceType).methods?.list.length ?? 0) === 0) {
        check(cursor, emptyInterface);
      }
    }
  },
});

const emptyInterface = "replace the empty interface with a concrete type, type parameter, or sum type, or keep it with //nolint:noany // <reason>";
const rawMessage = "decode into a concrete type instead of json.RawMessage, or keep it with //nolint:noany // <reason>";

// isRawMessage reports whether the object names json.RawMessage, directly or through an
// alias. Newer Go releases define json.RawMessage as an alias of jsontext.Value.
function isRawMessage(object: types.Object | null | undefined): boolean {
  if (object?.$type !== "TypeName") {
    return false;
  }
  const named = types.unalias(object.type());
  const typeName = named?.$type === "Named" ? named.obj() : null;
  const path = typeName?.pkg()?.path();
  return (path === "encoding/json" && typeName?.name() === "RawMessage") || (path === "encoding/json/jsontext" && typeName?.name() === "Value");
}

// Reports whether the type expression is the whole constraint of a type parameter.
function isConstraint(cursor: Cursor): boolean {
  if (cursor.parentEdgeKind() !== edge.Field_Type) {
    return false;
  }
  const kind = cursor.parent().parent().parentEdgeKind();
  return kind === edge.FuncType_TypeParams || kind === edge.TypeSpec_TypeParams;
}

// Forced finds signatures that another package requires to use the empty interface.
//
// Each check compares whole types, so it never exempts an any left to our choice.
class Forced {
  private interfaces: types.Interface[] | undefined;

  constructor(
    private readonly pkg: types.Package,
    private readonly info: types.Info,
    private readonly root: Cursor,
  ) {}

  covers(cursor: Cursor): boolean {
    const owner = signatureOwner(cursor);
    if (owner === undefined) {
      return false;
    }
    const fn = owner.node();
    if (fn?.$type === "FuncDecl" && fn.recv !== null && this.implementsImported(fn)) {
      return true;
    }
    return this.passedToForeign(owner) || this.forwardsVariadic(cursor, owner);
  }

  // implementsImported reports whether the method matches a method of an imported
  // interface that its receiver type implements.
  private implementsImported(decl: ast.FuncDecl): boolean {
    const method = decl.name === null ? undefined : this.info.defs.get(decl.name);
    if (method?.$type !== "Func") {
      return false;
    }
    const recv = method.signature()?.recv()?.type() ?? null;
    const named = recv?.$type === "Pointer" ? recv.elem() : recv;
    const pointer = types.newPointer(named);
    return this.importedInterfaces().some((iface) => {
      for (let i = 0; i < iface.numMethods(); i++) {
        const candidate = iface.method(i);
        if (candidate?.name() === method.name() && types.identical(candidate.type(), method.type())) {
          return types.implements_(pointer, iface);
        }
      }
      return false;
    });
  }

  private importedInterfaces(): types.Interface[] {
    if (this.interfaces === undefined) {
      this.interfaces = [];
      for (const imported of this.pkg.imports()) {
        const scope = imported?.scope();
        for (const name of scope?.names() ?? []) {
          const object = scope?.lookup(name);
          if (object?.$type !== "TypeName" || !object.exported()) {
            continue;
          }
          const declared = object.type();
          if (declared?.$type === "Named" && declared.typeParams() !== null) {
            continue;
          }
          const underlying = declared?.underlying();
          if (underlying?.$type === "Interface" && underlying.isMethodSet()) {
            this.interfaces.push(underlying);
          }
        }
      }
    }
    return this.interfaces;
  }

  // passedToForeign reports whether the function, or a use of a declared one, is a
  // value whose expected type is exactly another package's declared function type.
  private passedToForeign(owner: Cursor): boolean {
    const fn = owner.node();
    if (fn?.$type === "FuncLit") {
      return this.expectsForeign(owner, this.info.typeOf(fn));
    }
    const name = (fn as ast.FuncDecl).name;
    const object = name === null ? undefined : this.info.defs.get(name);
    if (object === null || object === undefined) {
      return false;
    }
    for (const cursor of this.root.preorder(ast.Ident)) {
      if (this.info.uses.get(cursor.node() as ast.Ident) !== object) {
        continue;
      }
      const value = cursor.parentEdgeKind() === edge.SelectorExpr_Sel ? cursor.parent() : cursor;
      if (this.expectsForeign(value, object.type())) {
        return true;
      }
    }
    return false;
  }

  private expectsForeign(value: Cursor, actual: types.Type | null): boolean {
    const expected = this.expectedType(value);
    return expected !== null && types.identical(expected.underlying(), actual);
  }

  // expectedType returns another package's declared type for a call argument, a
  // struct literal field, or an assigned field, and null anywhere else.
  private expectedType(value: Cursor): types.Type | null {
    const [kind, index] = value.parentEdge();
    const parent = value.parent().node();
    if (kind === edge.CallExpr_Args) {
      const call = parent as ast.CallExpr;
      const callee = typeutil.callee(this.info, call);
      if (callee?.$type !== "Func" || this.isLocal(callee)) {
        return null;
      }
      return parameterType(callee.origin()?.signature() ?? null, index, call.ellipsis !== token.NoPos);
    }
    if (kind === edge.KeyValueExpr_Value) {
      return this.foreignField((parent as ast.KeyValueExpr).key);
    }
    if (kind === edge.AssignStmt_Rhs) {
      const assign = parent as ast.AssignStmt;
      return assign.lhs.length === assign.rhs.length ? this.foreignField(assign.lhs[index]) : null;
    }
    return null;
  }

  private foreignField(expr: ast.Expr | null): types.Type | null {
    const ident = expr?.$type === "SelectorExpr" ? expr.sel : expr;
    const field = ident?.$type === "Ident" ? this.info.uses.get(ident) : undefined;
    if (field?.$type !== "Var" || !field.isField() || this.isLocal(field)) {
      return null;
    }
    return field.origin()?.type() ?? null;
  }

  // forwardsVariadic reports whether a final ...any parameter only feeds another
  // package's final ...any parameter.
  private forwardsVariadic(cursor: Cursor, owner: Cursor): boolean {
    if (cursor.parentEdgeKind() !== edge.Ellipsis_Elt || cursor.parent().parentEdgeKind() !== edge.Field_Type) {
      return false;
    }
    const field = cursor.parent().parent();
    const list = field.parent();
    const names = (field.node() as ast.Field).names;
    if (list.parentEdgeKind() !== edge.FuncType_Params || names.length !== 1 || names[0] === null) {
      return false;
    }
    const parameter = this.info.defs.get(names[0]);
    let forwarded = false;
    for (const use of owner.preorder(ast.Ident)) {
      if (this.info.uses.get(use.node() as ast.Ident) !== parameter) {
        continue;
      }
      if (!this.spreadsIntoForeign(use)) {
        return false;
      }
      forwarded = true;
    }
    return forwarded;
  }

  private spreadsIntoForeign(use: Cursor): boolean {
    const [kind, index] = use.parentEdge();
    if (kind !== edge.CallExpr_Args) {
      return false;
    }
    const call = use.parent().node() as ast.CallExpr;
    const callee = typeutil.callee(this.info, call);
    if (call.ellipsis === token.NoPos || index !== call.args.length - 1 || callee?.$type !== "Func" || this.isLocal(callee)) {
      return false;
    }
    const signature = callee.origin()?.signature() ?? null;
    const params = signature?.params();
    const last = params?.at(params.len() - 1)?.type() ?? null;
    const elem = last?.$type === "Slice" ? last.elem() : null;
    return signature?.variadic() === true && types.identical(elem, types.Universe?.lookup("any")?.type() ?? null);
  }

  private isLocal(object: types.Object): boolean {
    return object.pkg()?.path() === this.pkg.path();
  }
}

// signatureOwner returns the declaration or literal whose signature contains the
// cursor, or undefined when the cursor is anywhere else, such as in a body.
function signatureOwner(cursor: Cursor): Cursor | undefined {
  for (const funcType of cursor.enclosing(ast.FuncType)) {
    const kind = funcType.parentEdgeKind();
    if (kind === edge.FuncDecl_Type || kind === edge.FuncLit_Type) {
      return funcType.parent();
    }
  }
  return undefined;
}

// parameterType returns the declared type a call argument at index is assigned to.
function parameterType(signature: types.Signature | null, index: number, spread: boolean): types.Type | null {
  const params = signature?.params();
  if (params === null || params === undefined) {
    return null;
  }
  const count = params.len();
  if (signature?.variadic() !== true || index < count - 1) {
    return index < count ? (params.at(index)?.type() ?? null) : null;
  }
  const last = params.at(count - 1)?.type() ?? null;
  if (spread) {
    return last;
  }
  return last?.$type === "Slice" ? last.elem() : null;
}

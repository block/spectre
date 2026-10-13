import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as token from "go/token";
import * as types from "go/types";
import * as edge from "golang.org/x/tools/go/ast/edge";
import type { Cursor } from "golang.org/x/tools/go/ast/inspector";
import * as typeutil from "golang.org/x/tools/go/types/typeutil";

export default defineAnalyzer({
  name: "typeboundary",
  doc: `report named types converted at another package's boundary

A module type such as type TypeRef string converted to or from its underlying
type, where the other side is a parameter or result of a function in another
module package, means that function holds the same concept as a plain value.
Give it the named type, moving the type to a package both can import when an
import cycle prevents that. Values are followed through local variables
defined by := and through optional.Some.`,
  requires: [inspect],
  run(pass) {
    const module = pass.module?.path;
    if (module === undefined) {
      return;
    }
    const info = pass.typesInfo;
    const root = pass.resultOf(inspect).root();
    const inModule = (pkg: types.Package | null) => {
      const path = pkg?.path() ?? "";
      return path === module || path.startsWith(module + "/");
    };
    const isNamed = (type: types.Type | null) =>
      type?.$type === "Named" && inModule(type.obj()?.pkg() ?? null) && isBasic(type.underlying());
    const callee = (call: ast.CallExpr) => {
      const fn = typeutil.callee(info, call);
      return fn?.$type === "Func" ? fn : undefined;
    };
    const foreign = (call: ast.CallExpr) => {
      const fn = callee(call);
      const pkg = fn?.pkg() ?? null;
      return pkg?.path() !== pass.pkg.path() && inModule(pkg) ? fn : undefined;
    };

    // source returns the foreign function whose result an expression holds, directly or via :=.
    const source = (expr: ast.Expr | null): types.Func | undefined => {
      let value = ast.unparen(expr);
      if (value?.$type === "Ident") {
        const variable = info.uses.get(value);
        const definition = variable?.$type === "Var" ? root.findByPos(variable.pos(), variable.pos()) : undefined;
        const assign = definition?.parent().node();
        if (definition?.parentEdgeKind() !== edge.AssignStmt_Lhs || assign?.$type !== "AssignStmt" || assign.tok !== token.DEFINE) {
          return undefined;
        }
        value = ast.unparen(assign.rhs.length === 1 ? assign.rhs[0] : assign.rhs[definition.parentEdgeIndex()]);
      }
      return value?.$type === "CallExpr" ? foreign(value) : undefined;
    };

    // sink returns the foreign function a value is passed to as the same type, directly, via := or via Some.
    const sink = (cursor: Cursor): types.Func | undefined => {
      const [kind, index] = cursor.parentEdge();
      const parent = cursor.parent();
      const node = parent.node();
      if (kind === edge.CallExpr_Args && node?.$type === "CallExpr") {
        const fn = callee(node);
        if (fn?.name() === "Some" && fn.pkg()?.path() === "github.com/alecthomas/types/optional") {
          return sink(parent);
        }
        return types.identical(parameter(info, node, index), info.typeOf(cursor.node() as ast.Expr)) ? foreign(node) : undefined;
      }
      if (kind === edge.AssignStmt_Rhs && node?.$type === "AssignStmt" && node.tok === token.DEFINE) {
        const variable = info.defs.get(node.lhs[index] as ast.Ident);
        for (const [ident, object] of info.uses) {
          const use = object === variable ? root.findNode(ident) : undefined;
          const fn = use === undefined ? undefined : sink(use);
          if (fn !== undefined) {
            return fn;
          }
        }
      }
      return undefined;
    };

    for (const cursor of root.preorder(ast.CallExpr)) {
      const call = cursor.node() as ast.CallExpr;
      if (call.args.length !== 1 || !info.types.get(call.fun as ast.Expr)?.isType()) {
        continue;
      }
      const to = types.unalias(info.typeOf(call));
      const from = types.unalias(info.typeOf(call.args[0] as ast.Expr));
      const name = (type: types.Type | null) => types.typeString(type, (pkg) => pkg?.name() ?? "");
      const into = isNamed(to) && isBasic(from) ? source(call.args[0]) : undefined;
      if (into !== undefined) {
        pass.report({ pos: call.pos(), message: `${name(to)} is converted from the result of ${into.fullName()}; make it return ${name(to)}` });
      }
      const outOf = isNamed(from) && isBasic(to) ? sink(cursor) : undefined;
      if (outOf !== undefined) {
        pass.report({ pos: call.pos(), message: `${name(from)} is converted to pass to ${outOf.fullName()}; make it take ${name(from)}` });
      }
    }
  },
});

function isBasic(type: types.Type | null): boolean {
  return type?.$type === "Basic" && (type.info() & (types.IsString | types.IsInteger)) !== 0;
}

// parameter returns the type an argument is passed as, unpacking a variadic slice.
function parameter(info: types.Info, call: ast.CallExpr, index: number): types.Type | null {
  const signature = types.unalias(info.typeOf(call.fun));
  const params = signature?.$type === "Signature" ? signature.params() : null;
  if (signature?.$type !== "Signature" || params === null || params.len() === 0) {
    return null;
  }
  const last = params.len() - 1;
  const type = types.unalias(params.at(Math.min(index, last))?.type() ?? null);
  return signature.variadic() && index >= last && call.ellipsis === 0 && type?.$type === "Slice" ? type.elem() : type;
}

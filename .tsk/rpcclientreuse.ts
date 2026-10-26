import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as token from "go/token";
import * as types from "go/types";
import * as edge from "golang.org/x/tools/go/ast/edge";
import type { Cursor } from "golang.org/x/tools/go/ast/inspector";
import * as typeutil from "golang.org/x/tools/go/types/typeutil";

export default defineAnalyzer({
  name: "rpcclientreuse",
  doc: `report generated RPC clients constructed for use only within one call

A function that constructs a generated Connect client and only calls its methods
builds a new client every time it runs. Construct the client once, wrap it in a
struct with methods, and pass that in. A client that is returned, stored, passed
on, or captured by a function literal is allowed, because something else keeps
it. A constructor is a New<Service>Client in a package whose path ends in
connect, taking a connect.HTTPClient and returning its <Service>Client
interface. Tests, main functions and Run methods are exempt, because they run
once per test, process or command.`,
  requires: [inspect],
  run(pass) {
    const info = pass.typesInfo;
    const root = pass.resultOf(inspect).root();
    const skipped = new Set(
      pass.files
        .filter((file) => ast.isGenerated(file) || pass.fset.position(file.package).filename.endsWith("_test.go"))
        .map((file) => pass.fset.position(file.package).filename),
    );

    for (const cursor of root.preorder(ast.CallExpr)) {
      const call = cursor.node() as ast.CallExpr;
      const fn = typeutil.callee(info, call);
      if (fn?.$type !== "Func" || !isConstructor(fn) || skipped.has(pass.fset.position(call.pos()).filename)) {
        continue;
      }
      const owner = enclosingFunc(cursor);
      if (owner === undefined || isEntryPoint(pass.pkg, owner.node()) || !usedOnlyLocally(info, root, cursor, owner)) {
        continue;
      }
      pass.report({
        pos: call.pos(),
        message: `${fn.pkg()?.name()}.${fn.name()} builds a client on every call; construct the client once, wrap it in a struct with methods, and pass that in`,
      });
    }
  },
});

// isConstructor reports whether fn is a generated Connect client constructor.
function isConstructor(fn: types.Func): boolean {
  const name = fn.name();
  const signature = fn.signature();
  const params = signature?.params() ?? null;
  const results = signature?.results() ?? null;
  if (!/^New\w+Client$/.test(name) || !(fn.pkg()?.path() ?? "").endsWith("connect") || signature?.recv() !== null) {
    return false;
  }
  if (params === null || params.len() === 0 || results?.len() !== 1) {
    return false;
  }
  const result = types.unalias(results.at(0)?.type() ?? null);
  if (result?.$type !== "Named" || result.obj()?.name() !== name.slice(3) || result.obj()?.pkg() !== fn.pkg()) {
    return false;
  }
  const first = types.unalias(params.at(0)?.type() ?? null);
  return (
    result.underlying()?.$type === "Interface" &&
    first?.$type === "Named" &&
    first.obj()?.name() === "HTTPClient" &&
    first.obj()?.pkg()?.path() === "connectrpc.com/connect"
  );
}

function enclosingFunc(cursor: Cursor): Cursor | undefined {
  for (const owner of cursor.enclosing(ast.FuncDecl, ast.FuncLit)) {
    return owner;
  }
  return undefined;
}

// isEntryPoint reports whether a function runs once per process or command, so
// constructing a client there constructs it once.
function isEntryPoint(pkg: types.Package, node: ast.Node | null): boolean {
  if (node?.$type !== "FuncDecl" || node.name === null) {
    return false;
  }
  return node.recv === null ? pkg.name() === "main" && node.name.name === "main" : node.name.name === "Run";
}

// usedOnlyLocally reports whether a constructed client is only called, directly
// or through a local variable, within the function that constructs it.
function usedOnlyLocally(info: types.Info, root: Cursor, call: Cursor, owner: Cursor): boolean {
  if (isMethodCall(call)) {
    return true;
  }
  const [kind, index] = call.parentEdge();
  const parent = call.parent().node();
  let name: ast.Ident | null | undefined;
  if (kind === edge.AssignStmt_Rhs && parent?.$type === "AssignStmt" && parent.tok === token.DEFINE && parent.lhs.length === parent.rhs.length) {
    const lhs = parent.lhs[index];
    name = lhs?.$type === "Ident" ? lhs : undefined;
  } else if (kind === edge.ValueSpec_Values && parent?.$type === "ValueSpec" && parent.names.length === parent.values.length) {
    name = parent.names[index];
  }
  const variable = name === null || name === undefined ? undefined : info.defs.get(name);
  if (variable?.$type !== "Var") {
    return false;
  }
  let used = false;
  for (const [ident, object] of info.uses) {
    if (object !== variable) {
      continue;
    }
    // A use in a function literal may outlive this call, as in a returned closure.
    const use = root.findNode(ident);
    if (use === undefined || enclosingFunc(use)?.index() !== owner.index() || !isMethodCall(use)) {
      return false;
    }
    used = true;
  }
  return used;
}

// isMethodCall reports whether an expression is only the receiver of a method call.
function isMethodCall(cursor: Cursor): boolean {
  return cursor.parentEdgeKind() === edge.SelectorExpr_X && cursor.parent().parentEdgeKind() === edge.CallExpr_Fun;
}

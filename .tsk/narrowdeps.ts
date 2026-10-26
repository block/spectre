import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as types from "go/types";
import * as edge from "golang.org/x/tools/go/ast/edge";
import type { Cursor } from "golang.org/x/tools/go/ast/inspector";
import { isKongStruct, namedStruct } from "./structtag";

interface Config {
  /** The most distinct members a holder may use and still be reported. */
  maxUsed: number;
  /** The fewest members, methods and fields, a type needs before it is reported. */
  minMembers: number;
}

// usage records the members read through one holder, unless it is used for
// more than reads: passed on whole, written, or called for its behaviour.
interface Usage {
  members: Set<string>;
  depends: boolean;
}

export default defineAnalyzer<Config>({
  name: "narrowdeps",
  doc: `report parameters and fields that hold a large type for one piece of its data

A function parameter or struct field holding a pointer to a struct from another
package of this module, with many methods and fields, but which is only used to
read one or two of them, couples its code to everything else that type does.
Depend on the data instead: the field's value, or the getter's result.

A read is a field read or a call to a method without parameters that returns a
value. A holder is not reported when it is also used otherwise: passed on whole,
assigned, written through, or called for behaviour. A callee it is passed to is
reported first, which then exposes the caller. Kong config types are exempt,
because constructors take the whole config, and so are protobuf messages.`,
  requires: [inspect],
  config: { maxUsed: 2, minMembers: 5 },
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
    const isTest = (pos: number) => pass.fset.position(pos).filename.endsWith("_test.go");

    // Holders are the parameters and struct fields whose type is large enough to report.
    const holders = new Map<types.Var, { type: types.Named; kind: "parameter" | "field"; ident: ast.Ident }>();
    const consider = (ident: ast.Ident, kind: "parameter" | "field") => {
      const variable = info.defs.get(ident);
      if (variable?.$type !== "Var" || isTest(ident.pos())) {
        return;
      }
      // A struct held by value is data; only a pointer holds a shared object.
      const named = types.unalias(variable.type())?.$type === "Pointer" ? namedStruct(variable.type()) : null;
      if (named === null) {
        return;
      }
      const [type, struct] = named;
      const pkg = type.obj()?.pkg() ?? null;
      if (!inModule(pkg) || pkg?.path() === pass.pkg.path() || isKongStruct(struct) || isProtoMessage(type)) {
        return;
      }
      if (type.numMethods() + struct.numFields() >= pass.config.minMembers) {
        holders.set(variable, { type, kind, ident });
      }
    };
    // Function literals are skipped, because whoever calls them sets their signature.
    for (const cursor of root.preorder(ast.FuncDecl)) {
      const fn = (cursor.node() as ast.FuncDecl).type;
      for (const field of fn?.params?.list ?? []) {
        for (const name of field?.names ?? []) {
          if (name !== null) {
            consider(name, "parameter");
          }
        }
      }
    }
    for (const cursor of root.preorder(ast.StructType)) {
      const struct = cursor.node() as ast.StructType;
      for (const field of struct.fields?.list ?? []) {
        for (const name of field?.names ?? []) {
          if (name !== null) {
            consider(name, "field");
          }
        }
      }
    }

    const usages = new Map<types.Var, Usage>();
    const usage = (variable: types.Var) => {
      let found = usages.get(variable);
      if (found === undefined) {
        found = { members: new Set(), depends: false };
        usages.set(variable, found);
      }
      return found;
    };
    // isRead reports whether a selector through a holder only reads data from it.
    const isRead = (selector: Cursor) => {
      const selection = info.selections.get(selector.node() as ast.SelectorExpr);
      const kind = selector.parentEdgeKind();
      if (selection?.kind() === types.FieldVal) {
        return kind !== edge.AssignStmt_Lhs && kind !== edge.IncDecStmt_X && kind !== edge.UnaryExpr_X;
      }
      const signature = selection?.type();
      return (
        selection?.kind() === types.MethodVal &&
        signature?.$type === "Signature" &&
        (signature.params()?.len() ?? 0) === 0 &&
        (signature.results()?.len() ?? 0) > 0
      );
    };
    for (const [ident, object] of info.uses) {
      if (object?.$type !== "Var" || !holders.has(object)) {
        continue;
      }
      let cursor = root.findNode(ident);
      if (cursor === undefined) {
        continue;
      }
      const kind = cursor.parentEdgeKind();
      if (holders.get(object)?.kind === "field") {
        // Setting a field in a composite literal is not a use of what it holds.
        if (kind === edge.KeyValueExpr_Key) {
          continue;
        }
        if (kind !== edge.SelectorExpr_Sel) {
          usage(object).depends = true;
          continue;
        }
        cursor = cursor.parent();
      }
      const selector = cursor.parent();
      const parent = selector.node();
      if (cursor.parentEdgeKind() === edge.SelectorExpr_X && parent?.$type === "SelectorExpr" && parent.sel !== null && isRead(selector)) {
        usage(object).members.add(parent.sel.name);
      } else {
        usage(object).depends = true;
      }
    }

    for (const [variable, { type, kind, ident }] of holders) {
      const used = usages.get(variable);
      if (used === undefined || used.depends || used.members.size === 0 || used.members.size > pass.config.maxUsed) {
        continue;
      }
      const members = [...used.members].sort().join(" and ");
      pass.report({
        pos: ident.pos(),
        message: `${kind} ${ident.name} holds ${type.obj()?.name()} only to read ${members}; depend on that instead`,
      });
    }
  },
});

// Protobuf messages are plain data, so reading a few of their fields is their purpose.
function isProtoMessage(type: types.Named): boolean {
  for (let i = 0; i < type.numMethods(); i++) {
    if (type.method(i)?.name() === "ProtoReflect") {
      return true;
    }
  }
  return false;
}

import { defineAnalyzer, defineFact } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as types from "go/types";
import * as edge from "golang.org/x/tools/go/ast/edge";
import type { Cursor } from "golang.org/x/tools/go/ast/inspector";
import { namedStruct } from "./structtag";

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

// generated marks a type declared in a generated file. A dependent package has
// only the type, not its file, so the declaring package exports this fact.
const generated = defineFact<boolean>("generated");

export default defineAnalyzer<Config>({
  name: "narrowdeps",
  doc: `report parameters and fields that hold a large type for one piece of its data

A function parameter or struct field holding a struct from this module, with
many fields and methods, but which is only used to read one or two of them,
couples its code to everything else that type does. Depend on the data instead:
the field's value, or the getter's result.

A read is a field read or a call to a getter: a method without parameters that
returns something other than only an error. Getters do not count towards a
type's size, because they expose its fields again. A holder is not reported when
it is also used otherwise: passed on whole, assigned, written through, or called
for behaviour. A callee it is passed to is reported first, which then exposes
the caller. A method's parameter of its own receiver type is exempt, as the
other operand, and so are generated types, because their generator sets their
shape.`,
  requires: [inspect],
  facts: [generated],
  config: { maxUsed: 2, minMembers: 5 },
  run(pass) {
    const generatedFiles = new Set(pass.files.filter((file) => ast.isGenerated(file)).map((file) => pass.fset.position(file.pos()).filename));
    const scope = pass.pkg.scope();
    for (const name of scope?.names() ?? []) {
      const object = scope?.lookup(name);
      if (object?.$type === "TypeName" && generatedFiles.has(pass.fset.position(object.pos()).filename)) {
        pass.exportObjectFact(object, generated, true);
      }
    }
    const isGenerated = (type: types.Named) => {
      const object = type.obj();
      return object !== null && pass.importObjectFact(object, generated) === true;
    };

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
    // A parameter of its method's receiver type is the other operand, as in a.Less(b).
    const consider = (ident: ast.Ident, kind: "parameter" | "field", receiver: types.TypeName | null) => {
      const variable = info.defs.get(ident);
      if (variable?.$type !== "Var" || isTest(ident.pos())) {
        return;
      }
      const named = namedStruct(variable.type());
      if (named === null) {
        return;
      }
      const [type, struct] = named;
      if (!inModule(type.obj()?.pkg() ?? null) || isGenerated(type) || (receiver !== null && type.obj() === receiver)) {
        return;
      }
      // Getters expose the fields again, so only fields and other methods count.
      let size = struct.numFields();
      for (let i = 0; i < type.numMethods(); i++) {
        const signature = type.method(i)?.signature() ?? null;
        if (signature !== null && !isGetter(signature)) {
          size++;
        }
      }
      if (size >= pass.config.minMembers) {
        holders.set(variable, { type, kind, ident });
      }
    };
    // Function literals are skipped, because whoever calls them sets their signature.
    for (const cursor of root.preorder(ast.FuncDecl)) {
      const decl = cursor.node() as ast.FuncDecl;
      const fn = decl.name === null ? null : info.defs.get(decl.name);
      const recv = fn?.$type === "Func" ? (fn.signature()?.recv() ?? null) : null;
      const receiver = namedStruct(recv?.type() ?? null)?.[0].obj() ?? null;
      for (const field of decl.type?.params?.list ?? []) {
        for (const name of field?.names ?? []) {
          if (name !== null) {
            consider(name, "parameter", receiver);
          }
        }
      }
    }
    for (const cursor of root.preorder(ast.StructType)) {
      const struct = cursor.node() as ast.StructType;
      for (const field of struct.fields?.list ?? []) {
        for (const name of field?.names ?? []) {
          if (name !== null) {
            consider(name, "field", null);
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
    // extendsPath reports whether access is the operand of a further field, index, or dereference.
    const extendsPath = (access: Cursor) => {
      switch (access.parentEdgeKind()) {
        case edge.ParenExpr_X:
        case edge.IndexExpr_X:
        case edge.StarExpr_X:
          return true;
        case edge.SelectorExpr_X:
          return info.selections.get(access.parent().node() as ast.SelectorExpr)?.kind() === types.FieldVal;
        default:
          return false;
      }
    };
    // isRead reports whether a selector through a holder only reads data from it.
    const isRead = (selector: Cursor) => {
      const selection = info.selections.get(selector.node() as ast.SelectorExpr);
      if (selection?.kind() === types.FieldVal) {
        // A write can go through a longer path, as in holder.State.Count++.
        let access = selector;
        while (extendsPath(access)) {
          access = access.parent();
        }
        const kind = access.parentEdgeKind();
        return kind !== edge.AssignStmt_Lhs && kind !== edge.IncDecStmt_X && kind !== edge.UnaryExpr_X;
      }
      // A method value that is not called keeps the receiver for later calls.
      const signature = selection?.type();
      return (
        selection?.kind() === types.MethodVal &&
        selector.parentEdgeKind() === edge.CallExpr_Fun &&
        signature?.$type === "Signature" &&
        isGetter(signature)
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

// isGetter reports whether a method reads data: it takes nothing and returns
// something. A method returning only an error, such as Flush, acts instead.
function isGetter(signature: types.Signature): boolean {
  const results = signature.results();
  const count = results?.len() ?? 0;
  if ((signature.params()?.len() ?? 0) !== 0 || count === 0) {
    return false;
  }
  const errorType = types.Universe?.lookup("error")?.type() ?? null;
  return !(count === 1 && types.identical(results?.at(0)?.type() ?? null, errorType));
}

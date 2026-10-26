import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as token from "go/token";
import * as types from "go/types";
import * as edge from "golang.org/x/tools/go/ast/edge";
import type { Cursor } from "golang.org/x/tools/go/ast/inspector";
import { namedStruct } from "./structtag";

// A holder is the one struct with a field of a candidate type.
interface Holder {
  name: string;
  fields: number;
}

export default defineAnalyzer({
  name: "singleusestruct",
  doc: `report structs that only one field of one other struct in their package uses

A struct that is the type of exactly one field of one other struct in its
package, and that is referenced nowhere else in the package and has no methods,
is a leftover of sharing that has ended. Move its fields into the struct that
uses it.

The linter sees one package, so it cannot tell whether another package uses an
exported struct; keep one that it must with a //nolint comment. Uses in tests do
not count, because folding the struct only means updating them too. Generated
types are exempt.`,
  requires: [inspect],
  run(pass) {
    const info = pass.typesInfo;
    const root = pass.resultOf(inspect).root();
    const generated = new Set(pass.files.filter((file) => ast.isGenerated(file)).map((file) => pass.fset.position(file.pos()).filename));
    const isTest = (pos: token.Pos) => pass.fset.position(pos).filename.endsWith("_test.go");
    const excluded = (pos: token.Pos) => isTest(pos) || generated.has(pass.fset.position(pos).filename);

    const candidates = new Map<types.TypeName, Holder | null>();
    const scope = pass.pkg.scope();
    for (const name of scope?.names() ?? []) {
      const object = scope?.lookup(name);
      if (object?.$type !== "TypeName" || object.isAlias() || excluded(object.pos())) {
        continue;
      }
      const named = namedStruct(object.type())?.[0];
      if (named?.obj() === object && named.typeParams() === null && named.numMethods() === 0) {
        candidates.set(object, null);
      }
    }

    // Any reference other than one field of one package-level struct disqualifies a type.
    // Tests are ignored so that the package and its test variant agree.
    const disqualified = new Set<types.TypeName>();
    for (const [ident, object] of info.uses) {
      if (object?.$type !== "TypeName" || !candidates.has(object) || disqualified.has(object) || isTest(ident.pos())) {
        continue;
      }
      const cursor = root.findNode(ident);
      const holder = cursor === undefined ? null : holderOf(info, cursor, excluded);
      const previous = candidates.get(object) ?? null;
      if (holder === null || holder === object || (previous !== null && previous.name !== holder.name())) {
        disqualified.add(object);
        continue;
      }
      candidates.set(object, { name: holder.name(), fields: (previous?.fields ?? 0) + 1 });
    }

    for (const [object, holder] of candidates) {
      if (holder?.fields !== 1 || disqualified.has(object)) {
        continue;
      }
      pass.report({
        pos: object.pos(),
        message: `${object.name()} is used only by ${holder.name}; move its fields into ${holder.name}`,
      });
    }
  },
});

// holderOf returns the package-level struct type whose single field has the
// type that ident names, through one pointer, or null for any other use.
function holderOf(info: types.Info, ident: Cursor, excluded: (pos: token.Pos) => boolean): types.TypeName | null {
  let cursor = ident;
  if (cursor.parentEdgeKind() === edge.StarExpr_X) {
    cursor = cursor.parent();
  }
  if (cursor.parentEdgeKind() !== edge.Field_Type) {
    return null;
  }
  const fieldCursor = cursor.parent();
  const field = fieldCursor.node() as ast.Field;
  const list = fieldCursor.parent();
  if (field.names.length > 1 || fieldCursor.parentEdgeKind() !== edge.FieldList_List || list.parentEdgeKind() !== edge.StructType_Fields) {
    return null;
  }
  const struct = list.parent();
  if (struct.parentEdgeKind() !== edge.TypeSpec_Type) {
    return null;
  }
  const spec = struct.parent();
  if (spec.parent().parentEdgeKind() !== edge.File_Decls || excluded(field.pos())) {
    return null;
  }
  const name = (spec.node() as ast.TypeSpec).name;
  const holder = name === null ? null : info.defs.get(name);
  return holder?.$type === "TypeName" ? holder : null;
}

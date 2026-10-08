import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as types from "go/types";
import { lookupTag } from "./structtag";

const optionalPath = "github.com/alecthomas/types/optional";

export default defineAnalyzer({
  name: "optionalstyle",
  doc: `report non-dot imports of optional and Option fields tagged omitempty

Option reads as a language feature, so the optional package is always
dot-imported. encoding/json never treats a struct as empty, so an Option JSON
field must use omitzero to be omitted when absent.`,
  requires: [inspect],
  run(pass) {
    for (const file of pass.files) {
      for (const spec of file.imports) {
        if (spec?.path?.value === `"${optionalPath}"` && spec.name?.name !== ".") {
          pass.report({ pos: spec.pos(), message: `import ${optionalPath} as . "${optionalPath}"` });
        }
      }
    }
    for (const cursor of pass.resultOf(inspect).root().preorder(ast.StructType)) {
      const struct = pass.typesInfo.typeOf(cursor.node() as ast.StructType);
      if (struct?.$type !== "Struct") {
        continue;
      }
      for (let i = 0; i < struct.numFields(); i++) {
        const field = struct.field(i);
        const options = lookupTag(struct.tag(i), "json")?.split(",").slice(1) ?? [];
        if (field !== null && isOption(field.type()) && options.includes("omitempty")) {
          pass.report({ pos: field.pos(), message: `${field.name()} is an Option, so tag it omitzero, not omitempty` });
        }
      }
    }
  },
});

function isOption(type: types.Type | null): boolean {
  const named = types.unalias(type);
  if (named?.$type !== "Named") {
    return false;
  }
  const typeName = named.origin()?.obj();
  return typeName?.name() === "Option" && typeName.pkg()?.path() === optionalPath;
}

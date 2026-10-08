import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import { isKongStruct, lookupTag, namedStruct } from "./structtag";

export default defineAnalyzer({
  name: "kongembed",
  doc: `report package configs that main packages do not embed with embed:""

A field of a main package struct whose type is a Kong-tagged struct from another
package must have an embed:"" tag, so the package's flags join the CLI.`,
  requires: [inspect],
  run(pass) {
    if (pass.pkg.name() !== "main") {
      return;
    }
    for (const cursor of pass.resultOf(inspect).root().preorder(ast.StructType)) {
      const struct = pass.typesInfo.typeOf(cursor.node() as ast.StructType);
      if (struct?.$type !== "Struct") {
        continue;
      }
      for (let i = 0; i < struct.numFields(); i++) {
        const field = struct.field(i);
        const found = namedStruct(field?.type() ?? null);
        if (field === null || found === null || lookupTag(struct.tag(i), "embed") !== undefined) {
          continue;
        }
        const [named, config] = found;
        const typeName = named.obj();
        if (typeName?.pkg() !== pass.pkg && isKongStruct(config)) {
          pass.report({
            pos: field.pos(),
            message: `${field.name()} must have an embed:"" tag to embed ${typeName?.pkg()?.name()}.${typeName?.name()}`,
          });
        }
      }
    }
  },
});

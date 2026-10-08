import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import { isKongStruct, lookupTag } from "./structtag";

export default defineAnalyzer({
  name: "konghelp",
  doc: `report Kong flags, arguments, and commands without a help tag

Kong treats every exported field of a Kong-tagged struct as an option, so each
needs a non-empty help tag. Fields tagged embed:"" or kong:"-" and embedded
fields are groups or ignored, so they are skipped.`,
  requires: [inspect],
  run(pass) {
    for (const cursor of pass.resultOf(inspect).root().preorder(ast.StructType)) {
      const struct = pass.typesInfo.typeOf(cursor.node() as ast.StructType);
      if (struct?.$type !== "Struct" || !isKongStruct(struct)) {
        continue;
      }
      for (let i = 0; i < struct.numFields(); i++) {
        const field = struct.field(i);
        const tag = struct.tag(i);
        if (
          field === null ||
          !field.exported() ||
          field.embedded() ||
          lookupTag(tag, "embed") !== undefined ||
          lookupTag(tag, "kong") === "-"
        ) {
          continue;
        }
        if ((lookupTag(tag, "help") ?? "").trim() === "") {
          pass.report({ pos: field.pos(), message: `${field.name()} needs a help:"..." tag` });
        }
      }
    }
  },
});

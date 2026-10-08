import { defineAnalyzer } from "tsk";
import * as types from "go/types";

export default defineAnalyzer({
  name: "methodfile",
  doc: `report methods declared in a different file from their type

Keeping a type and all of its methods in one file makes the type's whole API
visible in one place.`,
  run(pass) {
    for (const file of pass.files) {
      const filename = pass.fset.position(file.package).filename;
      for (const decl of file.decls) {
        if (decl?.$type !== "FuncDecl" || decl.recv === null || decl.name === null) {
          continue;
        }
        const method = pass.typesInfo.defs.get(decl.name);
        if (method?.$type !== "Func") {
          continue;
        }
        let receiver = types.unalias(method.signature()?.recv()?.type() ?? null);
        if (receiver?.$type === "Pointer") {
          receiver = types.unalias(receiver.elem());
        }
        if (receiver?.$type !== "Named") {
          continue;
        }
        const typeName = receiver.obj();
        if (typeName === null) {
          continue;
        }
        const typeFile = pass.fset.position(typeName.pos()).filename;
        if (typeFile !== filename) {
          pass.report({
            pos: decl.name.pos(),
            message: `${typeName.name()}.${decl.name.name} must be declared in ${typeFile.split("/").pop()} with its type`,
          });
        }
      }
    }
  },
});

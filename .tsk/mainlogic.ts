import { defineAnalyzer } from "tsk";
import * as ast from "go/ast";

export default defineAnalyzer({
  name: "mainlogic",
  doc: `report functions in main packages other than main and Run methods

Application logic belongs in internal/. Keep a startup helper in a main package
with a //nolint:mainlogic comment explaining why it is not application logic.`,
  run(pass) {
    // Generated test main packages live in the build cache, not the source tree.
    if (pass.pkg.name() !== "main" || pass.pkg.path().endsWith(".test")) {
      return;
    }
    for (const file of pass.files) {
      if (pass.fset.position(file.package).filename.endsWith("_test.go")) {
        continue;
      }
      for (const decl of file.decls) {
        if (decl?.$type !== "FuncDecl" || decl.name === null) {
          continue;
        }
        const allowed = decl.recv === null ? decl.name.name === "main" : decl.name.name === "Run";
        if (!allowed) {
          pass.report({
            pos: decl.name.pos(),
            message: `${decl.name.name} must move to internal/: main packages may only declare main and Run methods`,
          });
        }
      }
    }
  },
});

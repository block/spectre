import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import { isKongStruct, lookupTag } from "./structtag";

export default defineAnalyzer({
  name: "kongenv",
  doc: `report env tags on Kong flags

kong.DefaultEnvars gives every flag an environment variable, so an explicit
env tag duplicates the generated name and can drift from it. Only env:"-",
which keeps a flag out of the environment, is allowed. A struct counts as Kong
only through keys other than env, so other libraries' env tags are left alone.
Test files are skipped because they may parse without DefaultEnvars.`,
  requires: [inspect],
  run(pass) {
    for (const cursor of pass.resultOf(inspect).root().preorder(ast.StructType)) {
      const node = cursor.node() as ast.StructType;
      if (pass.fset.position(node.pos()).filename.endsWith("_test.go")) {
        continue;
      }
      const struct = pass.typesInfo.typeOf(node);
      if (struct?.$type !== "Struct" || !isKongStruct(struct, ["env"])) {
        continue;
      }
      for (let i = 0; i < struct.numFields(); i++) {
        const field = struct.field(i);
        const env = lookupTag(struct.tag(i), "env");
        if (field === null || env === undefined || env === "-") {
          continue;
        }
        pass.report({
          pos: field.pos(),
          message: `remove the env:"${env}" tag from ${field.name()}; kong.DefaultEnvars names its variable`,
        });
      }
    }
  },
});

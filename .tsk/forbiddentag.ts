import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import { lookupTag } from "./structtag";

interface Rule {
  /** Struct tag key to forbid. */
  key: string;
  /** Values of the key that are allowed. */
  allow: string[];
  /** Why the tag is forbidden, appended to the finding. */
  reason: string;
}

interface Config {
  /** Forbidden struct tags. */
  rules: Rule[];
}

export default defineAnalyzer<Config>({
  name: "forbiddentag",
  doc: `report struct tags that a configured rule forbids

Each rule forbids a tag key on every struct field, except for the values it
allows.`,
  config: { rules: [] },
  requires: [inspect],
  run(pass) {
    for (const cursor of pass.resultOf(inspect).root().preorder(ast.StructType)) {
      const struct = pass.typesInfo.typeOf(cursor.node() as ast.StructType);
      if (struct?.$type !== "Struct") {
        continue;
      }
      for (let i = 0; i < struct.numFields(); i++) {
        const field = struct.field(i);
        for (const rule of pass.config.rules) {
          const value = lookupTag(struct.tag(i), rule.key);
          if (field === null || value === undefined || rule.allow.includes(value)) {
            continue;
          }
          pass.report({
            pos: field.pos(),
            message: `remove the ${rule.key}:"${value}" tag from ${field.name()}; ${rule.reason}`,
          });
        }
      }
    }
  },
});

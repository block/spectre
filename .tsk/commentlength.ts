import { defineAnalyzer } from "tsk";
import * as ast from "go/ast";

// Matches Go directives, such as //go:build and //nolint:x, but not spaced prose.
const directive = /^(line |extern |export |[a-z0-9]+:[a-z0-9])/;

export default defineAnalyzer({
  name: "commentlength",
  doc: `report comments longer than two lines

Blank comment lines and directives such as //go:build and //nolint do not
count. Generated files are skipped.`,
  run(pass) {
    for (const file of pass.files) {
      if (ast.isGenerated(file)) {
        continue;
      }
      for (const group of file.comments) {
        let lines = 0;
        let start: ast.Comment | null = null;
        for (const comment of group?.list ?? []) {
          if (comment === null) {
            continue;
          }
          const counted = proseLines(comment.text);
          if (counted > 0 && start === null) {
            start = comment;
          }
          lines += counted;
        }
        if (start !== null && lines > 2) {
          pass.report({ pos: start.pos(), message: "comment exceeds two lines" });
        }
      }
    }
  },
});

function proseLines(text: string): number {
  if (text.startsWith("//")) {
    const body = text.slice(2);
    return body.trim() === "" || directive.test(body) ? 0 : 1;
  }
  const body = text.slice(2, text.endsWith("*/") ? -2 : undefined);
  return body.split("\n").filter((line) => line.replace(/^\s*\* ?/, "").trim() !== "").length;
}

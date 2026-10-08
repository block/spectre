import { defineAnalyzer } from "tsk";
import * as ast from "go/ast";
import * as types from "go/types";
import * as typeutil from "golang.org/x/tools/go/types/typeutil";
import { isKongStruct, namedStruct } from "./structtag";

export default defineAnalyzer({
  name: "configdefaults",
  doc: `report Kong config constructors that do not call kong.ApplyDefaults

A package function that returns a Kong-tagged struct declared in its package
must fill it with kong.ApplyDefaults, so Kong tags stay the only source of
default values.`,
  run(pass) {
    for (const file of pass.files) {
      if (pass.fset.position(file.package).filename.endsWith("_test.go")) {
        continue;
      }
      for (const decl of file.decls) {
        if (decl?.$type !== "FuncDecl" || decl.recv !== null || decl.name === null || decl.body === null) {
          continue;
        }
        const fn = pass.typesInfo.defs.get(decl.name);
        if (fn?.$type !== "Func") {
          continue;
        }
        const config = kongResult(pass.pkg, fn);
        if (config !== null && !callsApplyDefaults(pass.typesInfo, decl.body)) {
          pass.report({
            pos: decl.name.pos(),
            message: `${decl.name.name} returns ${config} without calling kong.ApplyDefaults`,
          });
        }
      }
    }
  },
});

// Returns the name of the first Kong-tagged struct of pkg that fn returns, or null.
function kongResult(pkg: types.Package, fn: types.Func): string | null {
  for (const result of fn.signature()?.results()?.variables() ?? []) {
    const found = namedStruct(result?.type() ?? null);
    if (found === null) {
      continue;
    }
    const [named, struct] = found;
    if (named.obj()?.pkg() === pkg && isKongStruct(struct)) {
      return named.obj()?.name() ?? null;
    }
  }
  return null;
}

function callsApplyDefaults(info: types.Info, body: ast.BlockStmt): boolean {
  let found = false;
  ast.inspect(body, (node) => {
    if (!found && node?.$type === "CallExpr") {
      const callee = typeutil.callee(info, node);
      found = callee?.name() === "ApplyDefaults" && callee.pkg()?.path() === "github.com/alecthomas/kong";
    }
    return !found;
  });
  return found;
}

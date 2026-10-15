import { defineAnalyzer, walkType } from "tsk";
import * as token from "go/token";
import * as types from "go/types";

export default defineAnalyzer({
  name: "publicapi",
  doc: `report public API that exposes a type from an internal package

Outside modules cannot name internal types, so an exported declaration in a
public package of this module must not mention one, even through an alias.
Mirror the type in the public package instead.`,
  run(pass) {
    const module = pass.module?.path;
    const importPath = pass.pkg.path();
    if (module === undefined || pass.pkg.name() === "main" || isInternal(importPath)) {
      return;
    }
    if (importPath !== module && !importPath.startsWith(module + "/")) {
      return;
    }
    const exposesInternal = (pkg: types.Package | null) => {
      const path = pkg?.path() ?? "";
      return (path === module || path.startsWith(module + "/")) && isInternal(path);
    };
    // report flags every internal type that the given types mention.
    const report = (name: string, pos: token.Pos, roots: (types.Type | null)[]) => {
      const exposed = new Set<string>();
      const visit = (type: types.Named | types.Alias) => {
        const object = type.obj();
        if (exposesInternal(object?.pkg() ?? null)) {
          exposed.add(type.string());
          return false;
        }
        // An unexported local type has no declaration of its own to check, so its
        // exported members are checked here, including those promoted by embedding.
        const unexportedLocal = type.$type === "Named" && object?.pkg()?.path() === importPath && !object.exported();
        return unexportedLocal ? "declaration" : true;
      };
      for (const root of roots) {
        walkType(root, visit, { exported: true, types: [types.Named, types.Alias] });
      }
      for (const type of exposed) {
        pass.report({ pos, message: `${name} exposes internal type ${type}; mirror it in this package` });
      }
    };
    const scope = pass.pkg.scope();
    for (const name of scope?.names() ?? []) {
      const object = scope?.lookup(name) ?? null;
      if (object === null || !object.exported() || pass.fset.position(object.pos()).filename.endsWith("_test.go")) {
        continue;
      }
      const declared = object.type();
      if (object.$type === "TypeName" && !object.isAlias() && declared?.$type === "Named") {
        // Methods are walked separately so that each is reported where it is declared.
        report(name, object.pos(), [...(declared.typeParams()?.typeParams() ?? []), declared.underlying()]);
        for (const method of declared.methods()) {
          if (method?.exported()) {
            report(`${name}.${method.name()}`, method.pos(), [method.type()]);
          }
        }
      } else {
        report(name, object.pos(), [declared]);
      }
    }
  },
});

// isInternal reports whether an import path is inside an internal directory.
function isInternal(path: string): boolean {
  return path.split("/").includes("internal");
}

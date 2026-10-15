import { defineAnalyzer } from "tsk";
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
    // report flags every internal type that walk reaches from a declaration.
    const report = (name: string, pos: token.Pos, walk: (visit: (type: types.Type | null) => void) => void) => {
      const exposed = new Set<string>();
      const visit = (type: types.Type | null) => {
        switch (type?.$type) {
          case "Alias":
            if (exposesInternal(type.obj()?.pkg() ?? null)) {
              exposed.add(type.string());
              return;
            }
            visitList(type.typeArgs(), visit);
            visit(type.rhs());
            return;
          case "Named":
            if (exposesInternal(type.obj()?.pkg() ?? null)) {
              exposed.add(type.string());
              return;
            }
            // Another named type's own declaration is checked in its package.
            visitList(type.typeArgs(), visit);
            return;
          case "Pointer":
          case "Slice":
          case "Array":
          case "Chan":
            visit(type.elem());
            return;
          case "Map":
            visit(type.key());
            visit(type.elem());
            return;
          case "Signature":
            visitTypeParams(type.typeParams(), visit);
            visitTuple(type.params(), visit);
            visitTuple(type.results(), visit);
            return;
          case "Tuple":
            visitTuple(type, visit);
            return;
          case "Struct":
            for (let i = 0; i < type.numFields(); i++) {
              const field = type.field(i);
              if (field?.exported() || field?.embedded()) {
                visit(field.type());
              }
            }
            return;
          case "Interface":
            for (let i = 0; i < type.numExplicitMethods(); i++) {
              const method = type.explicitMethod(i);
              if (method?.exported()) {
                visit(method.type());
              }
            }
            for (let i = 0; i < type.numEmbeddeds(); i++) {
              visit(type.embeddedType(i));
            }
            return;
          case "TypeParam":
            visit(type.constraint());
            return;
          case "Union":
            for (let i = 0; i < type.len(); i++) {
              visit(type.term(i)?.type() ?? null);
            }
            return;
          case "Basic":
          case undefined:
            return;
        }
      };
      walk(visit);
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
        report(name, object.pos(), (visit) => {
          visitTypeParams(declared.typeParams(), visit);
          visit(declared.underlying());
        });
        for (let i = 0; i < declared.numMethods(); i++) {
          const method = declared.method(i);
          if (method?.exported()) {
            report(`${name}.${method.name()}`, method.pos(), (visit) => visit(method.type()));
          }
        }
      } else {
        report(name, object.pos(), (visit) => visit(declared));
      }
    }
  },
});

// isInternal reports whether an import path is inside an internal directory.
function isInternal(path: string): boolean {
  return path.split("/").includes("internal");
}

function visitList(list: types.TypeList | null, visit: (type: types.Type | null) => void) {
  for (let i = 0; i < (list?.len() ?? 0); i++) {
    visit(list?.at(i) ?? null);
  }
}

function visitTypeParams(list: types.TypeParamList | null, visit: (type: types.Type | null) => void) {
  for (let i = 0; i < (list?.len() ?? 0); i++) {
    visit(list?.at(i)?.constraint() ?? null);
  }
}

function visitTuple(tuple: types.Tuple | null, visit: (type: types.Type | null) => void) {
  for (let i = 0; i < (tuple?.len() ?? 0); i++) {
    visit(tuple?.at(i)?.type() ?? null);
  }
}

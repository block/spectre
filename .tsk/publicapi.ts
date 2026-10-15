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
      // Type parameters and local types can refer to themselves, so each
      // declaration is walked once, keyed by where it is declared.
      const visited = new Set<string>();
      const firstVisit = (object: types.TypeName | null, key = "") => {
        const id = `${object?.pos() ?? token.NoPos}${key}`;
        if (visited.has(id)) {
          return false;
        }
        visited.add(id);
        return true;
      };
      // descend checks an unexported local type, which has no declaration of its
      // own to check. Methods promoted by embedding are checked on the embedder.
      const descend = (named: types.Named, withMethods: boolean) => {
        const origin = named.origin();
        const object = origin?.obj() ?? null;
        if (origin === null || object?.pkg()?.path() !== importPath || object.exported() || !firstVisit(object, `:${withMethods}`)) {
          return;
        }
        visitTypeParams(origin.typeParams(), visit);
        visit(origin.underlying());
        if (withMethods) {
          for (const method of exportedMethods(origin)) {
            visit(method.type());
          }
        }
      };
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
            visitList(type.typeArgs(), visit);
            descend(type, true);
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
              const embedded = field?.embedded() ? embeddedNamed(field.type()) : null;
              if (embedded === null) {
                if (field?.exported()) {
                  visit(field.type());
                }
              } else if (exposesInternal(embedded.obj()?.pkg() ?? null)) {
                exposed.add(embedded.string());
              } else {
                visitList(embedded.typeArgs(), visit);
                descend(embedded, false);
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
            if (firstVisit(type.obj())) {
              visit(type.constraint());
            }
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
    // exportedMethods returns the exported methods declared in this package that
    // a type has, including those promoted from embedded types.
    const exportedMethods = (named: types.Named) => {
      const methods: types.Func[] = [];
      const set = types.newMethodSet(types.newPointer(named));
      for (let i = 0; i < (set?.len() ?? 0); i++) {
        const method = set?.at(i)?.obj() ?? null;
        if (method?.$type === "Func" && method.exported() && method.pkg()?.path() === importPath) {
          methods.push(method);
        }
      }
      return methods;
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
        for (const method of exportedMethods(declared)) {
          report(`${name}.${method.name()}`, method.pos(), (visit) => visit(method.type()));
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

// embeddedNamed returns the named type an embedded field holds, through a pointer.
function embeddedNamed(type: types.Type | null): types.Named | null {
  const unaliased = types.unalias(type);
  const target = unaliased?.$type === "Pointer" ? types.unalias(unaliased.elem()) : unaliased;
  return target?.$type === "Named" ? target : null;
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

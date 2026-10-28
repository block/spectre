import { defineAnalyzer } from "tsk";

// Module-relative path of the command line, which owns all terminal output.
const cliPackage = "internal/cli";
const terminalPackage = cliPackage + "/terminal";

export default defineAnalyzer({
  name: "architecture",
  doc: `report code outside the layer that owns it

Main packages may declare only main and Run methods, because application logic
belongs in internal/. Keep a startup helper in a main package with a
//nolint:architecture comment explaining why it is not application logic.

Only ${cliPackage} and its sub-packages may import ${terminalPackage}, because
terminal output is the domain of the command line. Domain packages return data.`,
  run(pass) {
    // Generated test main packages live in the build cache, not the source tree.
    if (pass.pkg.path().endsWith(".test")) {
      return;
    }
    const modulePath = pass.module?.path;
    const relative = (importPath: string) =>
      modulePath !== undefined && importPath.startsWith(modulePath + "/") ? importPath.slice(modulePath.length + 1) : importPath;
    // An external test package belongs to the package it tests.
    const owner = relative(pass.pkg.path()).replace(/_test$/, "");
    const inCLI = owner === cliPackage || owner.startsWith(cliPackage + "/");
    for (const file of pass.files) {
      if (!inCLI) {
        for (const spec of file.imports) {
          const path = spec?.path?.value.slice(1, -1);
          if (spec !== null && path !== undefined && relative(path) === terminalPackage) {
            pass.report({
              pos: spec.pos(),
              message: `only ${cliPackage} may import ${path}: return data and let the command line write it`,
            });
          }
        }
      }
      if (pass.pkg.name() !== "main" || pass.fset.position(file.package).filename.endsWith("_test.go")) {
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

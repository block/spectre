import { defineAnalyzer } from "tsk";
import { inspect } from "tsk/passes";
import * as ast from "go/ast";
import * as typeutil from "golang.org/x/tools/go/types/typeutil";

// Module-relative path of the command line, which owns all terminal output.
const cliPackage = "internal/cli";
const terminalPackage = cliPackage + "/terminal";
// Module-relative paths of the SPECTRE service protocol's generated packages.
const protocolPackages = ["spectrepb", "spectrepb/spectrepbconnect"];
// The server's API boundary, which converts protocol messages to domain types.
const protocolBoundary = ["internal/server", "internal/server/wire", "internal/server/client"];

// Module-relative path of the interface that protocols implement, and the prefix of the protocols' own packages.
const protocolsPackage = "internal/protocols";
const protocolSubpackages = protocolsPackage + "/";
// Non-test packages besides the root package that may import a protocol's package:
// the integration harness starts a mirror of the real protocols.
const protocolImporters = ["internal/integration/it"];

export default defineAnalyzer({
  name: "architecture",
  doc: `report code outside the layer that owns it

Main packages may declare only main and Run methods, because application logic
belongs in internal/. Keep a startup helper in a main package with a
//nolint:architecture comment explaining why it is not application logic.

Only ${cliPackage} and its sub-packages may import ${terminalPackage}, because
terminal output is the domain of the command line. Domain packages return data.

Only the SPECTRE server's API boundary, ${protocolBoundary.join(", ")}, may
import ${protocolPackages.join(" or ")}, because protocol messages stop at the
API. Everything else uses internal/model types, which the boundary converts.

Only the root package and tests may import the sub-packages of ${protocolsPackage}, which
hold the protocols themselves, because the root package registers them and
everything else works through the ${protocolsPackage} interface. A protocol's own
sub-packages and ${protocolImporters.join(", ")} are the exceptions.

Storage adapters persist aggregates. Domain transitions belong in the storage
repository, so backend files must not call methods from internal/model.`,
  requires: [inspect],
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
    const mayImportProtocols = pass.pkg.path().replace(/_test$/, "") === modulePath || owner.startsWith(protocolSubpackages) || protocolImporters.includes(owner);
    const atProtocolBoundary = protocolBoundary.includes(owner) || protocolPackages.includes(owner);
    if (owner === "internal/server/storage") {
      for (const cursor of pass.resultOf(inspect).root().preorder(ast.CallExpr)) {
        const call = cursor.node() as ast.CallExpr;
        const filename = pass.fset.position(call.pos()).filename;
        if (/(?:repository|storage)(?:_test)?\.go$|_test\.go$/.test(filename)) {
          continue;
        }
        const fn = typeutil.callee(pass.typesInfo, call);
        if (fn?.$type === "Func" && relative(fn.pkg()?.path() ?? "") === "internal/model" && fn.signature()?.recv() !== null) {
          pass.report({ pos: call.pos(), message: "storage adapters persist aggregates; apply domain transitions in the repository" });
        }
      }
    }
    for (const file of pass.files) {
      const inTestFile = pass.fset.position(file.package).filename.endsWith("_test.go");
      for (const spec of file.imports) {
        const path = spec?.path?.value.slice(1, -1);
        if (spec === null || path === undefined) {
          continue;
        }
        if (!inCLI && relative(path) === terminalPackage) {
          pass.report({
            pos: spec.pos(),
            message: `only ${cliPackage} may import ${path}: return data and let the command line write it`,
          });
        }
        if (!mayImportProtocols && relative(path).startsWith(protocolSubpackages) && !inTestFile) {
          pass.report({
            pos: spec.pos(),
            message: `only the root package and tests may import ${path}: work through ${protocolsPackage}, which the root package registers protocols with`,
          });
        }
        if (!atProtocolBoundary && protocolPackages.includes(relative(path))) {
          pass.report({
            pos: spec.pos(),
            message: `only the SPECTRE server API boundary may import ${path}: use internal/model types, converted by internal/server/wire`,
          });
        }
      }
      if (pass.pkg.name() !== "main" || inTestFile) {
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

import { defineAnalyzer } from "tsk";
import * as fs from "io/fs";
import * as os from "os";
import * as filepath from "path/filepath";

interface Config {
  /** Import paths of packages that may live outside cmd/ and internal/, such as a public API. */
  allow: string[];
}

// Every other binary's scripts/ entry is a symlink to this launcher, which builds the binary its name selects.
const launcher = "spectre-ingress";

export default defineAnalyzer<Config>({
  name: "layout",
  doc: `report packages outside cmd/<command> and internal/

A main package must be at cmd/<command> and have scripts/<command>, a symlink
to the ${launcher} launcher. Every other package must be under internal/,
unless the allow option lists it. Paths are relative to the module root.`,
  config: { allow: [] },
  run(pass) {
    const importPath = pass.pkg.path();
    // Generated test main packages live in the build cache, not the source tree.
    if (importPath.endsWith(".test") || pass.config.allow.includes(importPath)) {
      return;
    }
    // Test files are skipped so external test packages follow the package they test.
    const files = pass.files
      .map((file) => ({ file, filename: pass.fset.position(file.package).filename }))
      .filter(({ filename }) => !filename.endsWith("_test.go"))
      .sort((a, b) => (a.filename < b.filename ? -1 : 1));
    if (files.length === 0) {
      return;
    }
    const { file, filename } = files[0];
    const modulePath = pass.module?.path;
    let relative = importPath;
    if (modulePath !== undefined) {
      relative = importPath === modulePath ? "" : importPath.slice(modulePath.length + 1);
    }
    const report = (message: string) => pass.report({ pos: file.package, message });
    if (pass.pkg.name() !== "main") {
      if (relative !== "internal" && !relative.startsWith("internal/")) {
        report(`package ${importPath} must be under internal/`);
      }
      return;
    }
    const match = /^cmd\/([^/]+)$/.exec(relative);
    if (match === null) {
      report(`main package ${importPath} must be at cmd/<command>`);
      return;
    }
    const command = match[1];
    const moduleDir = filepath.dir(filepath.dir(filepath.dir(filename)));
    const script = filepath.join(moduleDir, "scripts", command);
    let info: fs.FileInfo | null;
    try {
      info = os.lstat(script);
    } catch (err) {
      if (err instanceof os.ErrNotExist) {
        report(`missing scripts/${command}, a symlink to ${launcher}`);
        return;
      }
      throw err;
    }
    if (command === launcher) {
      return;
    }
    const isSymlink = info !== null && (info.mode() & fs.ModeSymlink) !== 0;
    if (!isSymlink || os.readlink(script) !== launcher) {
      report(`scripts/${command} must be a symlink to ${launcher}`);
    }
  },
});

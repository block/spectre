import * as types from "go/types";

// Options are parsed like reflect.StructTag.Lookup, which scripts cannot call.
const tagPattern = /([^\s:"]+):"((?:[^"\\]|\\.)*)"/g;

/** Returns the value of key in a struct tag, or undefined when the key is absent. */
export function lookupTag(tag: string, key: string): string | undefined {
  for (const match of tag.matchAll(tagPattern)) {
    if (match[1] === key) {
      return match[2];
    }
  }
  return undefined;
}

// Keys that only Kong reads, so a struct using one is a set of command-line options.
const kongKeys = [
  "arg",
  "cmd",
  "default",
  "embed",
  "enum",
  "env",
  "group",
  "help",
  "hidden",
  "name",
  "negatable",
  "placeholder",
  "prefix",
  "required",
  "short",
  "type",
  "xor",
];

/** Reports whether any field of a struct has a Kong tag. */
export function isKongStruct(struct: types.Struct): boolean {
  for (let i = 0; i < struct.numFields(); i++) {
    const tag = struct.tag(i);
    if (kongKeys.some((key) => lookupTag(tag, key) !== undefined)) {
      return true;
    }
  }
  return false;
}

/** Returns the struct underlying a named type, through one pointer, or null. */
export function namedStruct(type: types.Type | null): [types.Named, types.Struct] | null {
  let target = types.unalias(type);
  if (target?.$type === "Pointer") {
    target = types.unalias(target.elem());
  }
  if (target?.$type !== "Named") {
    return null;
  }
  const underlying = target.underlying();
  if (underlying?.$type !== "Struct") {
    return null;
  }
  return [target, underlying];
}

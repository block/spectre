// Spectre's script API. Type arguments name schema types and field paths; Spectre
// reads them from the type checker when it loads scripts, so they must be explicit.
declare module "spectre" {
  type Normaliser<V> = (value: V) => V | undefined;

  // Same is exact type identity, so a structurally similar type is not mistaken
  // for an ancestor when cutting off recursive paths.
  type Same<A, B> = (<G>() => G extends A ? 1 : 2) extends <G>() => G extends B ? 1 : 2 ? true : false;
  type Visited<T, Ancestors extends unknown[]> = Ancestors extends [infer Head, ...infer Rest]
    ? Same<T, Head> extends true
      ? true
      : Visited<T, Rest>
    : false;

  type Element<V> = V extends readonly (infer E)[] ? E : V;
  type Segment<K extends string, V> = V extends readonly unknown[] ? `${K}[]` : K;
  // The runtime splits paths on dots and reads [] as list descent.
  type Unaddressable = "" | `${string}.${string}` | `${string}[]`;

  // Paths descend through objects and list elements, but not maps or cycles.
  type Nested<V, Prefix extends string, Missing, Ancestors extends unknown[]> = V extends object
    ? string extends keyof V
      ? never
      : Visited<V, Ancestors> extends true
        ? never
        : Fields<V, Prefix, Missing, Ancestors>
    : never;

  // Fields pairs each path with the value its normaliser receives, which may be
  // undefined when the field or any ancestor is optional.
  type Fields<T, Prefix extends string = "", Missing = never, Ancestors extends unknown[] = []> = {
    [K in keyof T & string]-?: K extends Unaddressable
      ? never
      :
          | [`${Prefix}${K}`, T[K] | Missing]
          | Nested<
              Element<NonNullable<T[K]>>,
              `${Prefix}${Segment<K, NonNullable<T[K]>>}.`,
              Missing | (undefined extends T[K] ? undefined : never),
              [T, ...Ancestors]
            >;
  }[keyof T & string];

  /** A JSON field path of T, with "[]" after each list. */
  export type FieldPath<T> = Fields<T>[0];
  /** The value a normaliser for path P of T receives and returns. */
  export type FieldValue<T, P> = Extract<Fields<T>, [P, unknown]>[1];

  /** The proxied service's inbound endpoints. */
  export const ingress: {
    /** Types the service's responses for requests matching "<METHOD> /<path>". */
    match<T extends object>(protocol: "http", pattern: string): void;
    /** Skips comparison for requests matching "<METHOD> /<path>"; they are never quarantined. */
    ignore(protocol: "http", pattern: string): void;
  };
  /** The service's outbound calls to its dependencies. */
  export const egress: {
    /** Types requests to a dependency matching "<METHOD> <host>/<path>". */
    match<T extends object>(protocol: "http", pattern: string): void;
  };
  /** Normalises field P of every T; returning undefined removes it. */
  export function field<T extends object, P extends FieldPath<T>>(normalise: Normaliser<FieldValue<T, P>>): void;
  /** Normalises every T; returning undefined removes it. */
  export function message<T extends object>(normalise: Normaliser<T | undefined>): void;
}

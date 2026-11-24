// Spectre's script API. Type arguments name schema types and field paths; Spectre
// reads them from the type checker when it loads scripts, so they must be explicit.
declare module "spectre" {
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

  /** A body compared as exact text, without parsing or validation. */
  export interface Text {
    readonly __spectreText: never;
  }

  /** The proxied service's inbound endpoints. Each protocol adds its own members. */
  export interface Ingress {}
  /** The service's outbound calls to its dependencies. Each protocol adds its own members. */
  export interface Egress {}
  export const ingress: Ingress;
  export const egress: Egress;
  /** Normalises field P of every T, a normalised type; returning undefined removes an optional field. */
  export function field<T extends object, P extends FieldPath<T>>(normalise: (value: FieldValue<T, P>) => FieldValue<T, P>): void;
  /** Declares N, T with some fields made optional, as T's normalised type, and normalises every T. */
  export function message<T extends object, N extends object = T>(normalise?: (value: N | undefined) => N | undefined): void;
  /** Returns the value of a script variable, such as an environment's host name. It throws if the variable is unset. */
  export function variable(name: string): string;
}

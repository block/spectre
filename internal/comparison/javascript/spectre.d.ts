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

  /** A body compared as exact text, without parsing or validation. */
  export interface Text {
    readonly __spectreText: never;
  }

  /** An inbound HTTP request, as a rule's request function sees it. */
  export interface HTTPRequest {
    readonly method: string;
    /** The escaped path, without the query. */
    readonly path: string;
  }

  /** The comparable parts of a response. Members left out are not compared. */
  export interface Response<T> {
    status?: number;
    body?: T extends Text ? string : T;
  }

  /** Selects responses; rules apply in declaration order, and the first that selects a response decides. */
  export interface IngressHTTPRule {
    /** Narrows the rule to responses with one of these statuses, or whose status the function accepts. */
    status(...codes: number[]): IngressHTTPRule;
    status(select: (status: number) => boolean): IngressHTTPRule;
    /** Decodes the selected responses' bodies as T, then normalises each response. */
    match<T extends object>(normalise?: (response: Response<T>) => Response<T>): void;
    /** Skips comparison of the selected responses; they are never quarantined. */
    ignore(): void;
  }

  /** The proxied service's inbound endpoints. */
  export const ingress: {
    /** Starts a rule for requests matching "[<METHOD> ]/<path>", or that the function accepts. */
    http(select: string | ((request: HTTPRequest) => boolean)): IngressHTTPRule;
  };
  /** Selects a dependency's requests by pattern. */
  export interface EgressHTTPRule {
    /** Decodes the selected requests' bodies as T. */
    match<T extends object>(): void;
  }
  /** The service's outbound calls to its dependencies. */
  export const egress: {
    /** Starts a rule for requests to a dependency matching "<METHOD> <host>/<path>". */
    http(pattern: string): EgressHTTPRule;
  };
  /** Normalises field P of every T; returning undefined removes it. */
  export function field<T extends object, P extends FieldPath<T>>(normalise: Normaliser<FieldValue<T, P>>): void;
  /** Normalises every T; returning undefined removes it. */
  export function message<T extends object>(normalise: Normaliser<T | undefined>): void;
}

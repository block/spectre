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
    /** Header values by lower-case name, with repeated values joined by ", ". */
    readonly headers: Readonly<Record<string, string>>;
  }

  /** Selects by a header's value, with repeated values joined by ", ", or by a function of it. */
  type HeaderSelect = string | ((value: string | undefined) => boolean);

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
    /**
     * Narrows the rule to requests whose header equals the value, or that the function accepts; both sides
     * receive the same request. The function receives undefined for a missing header. Names ignore case.
     */
    requestHeader(name: string, select: HeaderSelect): IngressHTTPRule;
    /** Narrows the rule to responses whose header matches, as requestHeader does. Each side selects by its own. */
    responseHeader(name: string, select: HeaderSelect): IngressHTTPRule;
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
    /**
     * Lets the candidate leave out the reference's requests to this endpoint: one that no candidate
     * request claims within the match window expires without quarantining. Candidate requests still
     * pair as before.
     */
    optional(): EgressHTTPRule;
    /** Decodes the selected requests' bodies as T. */
    match<T extends object>(): void;
    /**
     * Declares that the candidate's requests to this endpoint replace the reference's requests to the
     * source endpoint, which must be declared with match<S>(), and decodes the source's responses as U.
     */
    replaces<S extends object, U extends object>(source: string): EgressReplacement<S, U>;
  }
  /** Translates between a replacing endpoint and the source endpoint it replaces. */
  export interface EgressReplacement<S, U> {
    /**
     * Decodes the selected requests' bodies as T and answers them with an R. request maps a candidate
     * request to the source request it pairs with, as match pairs requests; response builds the answer
     * from the candidate request and the paired reference response. Pairing never claims the reference
     * request, so it still pairs with a candidate request to the source.
     */
    using<T extends object, R extends object>(
      request: (request: T) => S,
      response: (request: T, reference: U) => R,
    ): void;
  }
  /** A value of a MySQL statement or reply, as text, with NULL as null. */
  export type SQLValue = string | null;
  /** A value a script gives a statement, which MySQL reads as text. */
  export type SQLArgument = SQLValue | number | boolean;
  /** A row of a MySQL reply by column name: the AS name, or the column's own name. */
  export type Row = Record<string, SQLValue>;
  /** A statement pattern whose rows are R. MySQL sends every field as text or NULL. */
  export type Statement<R> = [R[keyof R]] extends [SQLValue] ? string : "row fields must be string | null";
  /** One statement pattern per row type in T. */
  export type Statements<T extends object[]> = { [I in keyof T]: Statement<T[I]> };
  /** Claims the reply of each reference statement with these values, and returns its rows. */
  export type Claims<T extends object[]> = { [I in keyof T]: (...values: SQLArgument[]) => T[I][] };
  /** Offers rows as the reply to each candidate statement with the values before them. */
  export type Offers<T extends object[]> = { [I in keyof T]: (...offered: [...values: SQLArgument[], rows: T[I][]]) => void };
  /**
   * The service's MySQL statements, written with "?" in place of each value. Patterns match statements with
   * any values, whatever their comments or spacing. Type arguments name each statement's rows.
   */
  export interface EgressMySQL {
    /** Starts a replacement by one candidate statement, answered from the reference statements it replaces. */
    statement<R extends object = Row>(statement: NoInfer<Statement<R>>): EgressMySQLStatement<R>;
    /** Starts a replacement by candidate statements, each answered from the one reference statement they replace. */
    statements<T extends object[] = Row[]>(...statements: NoInfer<Statements<T>>): EgressMySQLStatements<T>;
  }
  /** Names the reference's statements that one candidate statement replaces. */
  export interface EgressMySQLStatement<R extends object> {
    replaces<T extends object[] = Row[]>(...statements: NoInfer<Statements<T>>): EgressMySQLCombination<R, T>;
  }
  /** Names the one reference statement that the candidate's statements replace. */
  export interface EgressMySQLStatements<T extends object[]> {
    replaces<R extends object = Row>(statement: NoInfer<Statement<R>>): EgressMySQLSplit<R, T>;
  }
  /** Answers a candidate statement from the reference statements it claims. */
  export interface EgressMySQLCombination<R extends object, T extends object[]> {
    /**
     * Runs for each candidate statement that matches, with its values and one claim per reference statement,
     * and returns the candidate's rows. Each claimed reference reply answers no other statement.
     */
    using(combine: (values: SQLValue[], claims: Claims<T>) => R[]): void;
  }
  /** Splits a reference statement's reply into replies to the candidate's statements. */
  export interface EgressMySQLSplit<R extends object, T extends object[]> {
    /**
     * Runs for each reference statement that matches, with its values and rows, and offers replies through
     * one offer per candidate statement. A matching candidate statement claims the reply offered with its values.
     */
    using(split: (values: SQLValue[], rows: R[], offers: Offers<T>) => void): void;
  }
  /** The service's outbound calls to its dependencies. */
  export const egress: {
    /** Starts a rule for requests to a dependency matching "<METHOD> <host>/<path>". */
    http(pattern: string): EgressHTTPRule;
    /** The service's MySQL statements. */
    mysql: EgressMySQL;
  };
  /** Normalises field P of every T; returning undefined removes it. */
  export function field<T extends object, P extends FieldPath<T>>(normalise: Normaliser<FieldValue<T, P>>): void;
  /** Normalises every T; returning undefined removes it. */
  export function message<T extends object>(normalise: Normaliser<T | undefined>): void;
  /** Returns the value of a script variable, such as an environment's host name. It throws if the variable is unset. */
  export function variable(name: string): string;
}

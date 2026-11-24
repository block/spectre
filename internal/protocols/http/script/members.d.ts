// The HTTP part of the spectre script API. It augments the shared Ingress and Egress interfaces.
declare module "spectre" {
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
    /** Narrows the rule to requests whose header matches, ignoring name case. Both sides see the same request. */
    requestHeader(name: string, select: HeaderSelect): IngressHTTPRule;
    /** Narrows the rule to responses whose header matches, as requestHeader does. Each side selects by its own. */
    responseHeader(name: string, select: HeaderSelect): IngressHTTPRule;
    /** Decodes the selected responses' bodies as T, Text, or unknown, then normalises each response. */
    match<T>(normalise?: (response: Response<T>) => Response<T>): void;
    /** Skips comparison of the selected responses; they are never quarantined. */
    ignore(): void;
  }

  export interface Ingress {
    /** Starts a rule for requests matching "[<METHOD> ]/<path>", or that the function accepts. */
    http(select: string | ((request: HTTPRequest) => boolean)): IngressHTTPRule;
  }
  /** Selects a dependency's requests by pattern. */
  export interface EgressHTTPRule {
    /** Lets reference requests here that no candidate request claims in the match window expire unquarantined. */
    optional(): EgressHTTPRule;
    /** Decodes the selected requests' bodies as T. */
    match<T extends object>(): void;
    /** Replaces reference requests to source, declared with match<S>(), and decodes its responses as U. */
    replaces<S extends object, U extends object>(source: string): EgressReplacement<S, U>;
  }
  /** Translates between a replacing endpoint and the source endpoint it replaces. */
  export interface EgressReplacement<S, U> {
    /** Answers requests decoded as T with an R; request maps each to its source request, response builds the R. */
    using<T extends object, R extends object>(
      request: (request: T) => S,
      response: (request: T, reference: U) => R,
    ): void;
  }
  export interface Egress {
    /** Starts a rule for requests to a dependency matching "<METHOD> <host>/<path>". */
    http(pattern: string): EgressHTTPRule;
  }
}

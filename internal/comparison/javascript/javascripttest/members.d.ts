// The members of the fake protocol's script binding. It only has egress, which pairs by endpoint.
declare module "spectre" {
  /** Selects a dependency's requests by pattern. */
  export interface FakeEgressRule {
    /** Lets the candidate leave out the reference's requests to this endpoint. */
    optional(): FakeEgressRule;
    /** Decodes the selected requests' bodies as T. */
    match<T extends object>(): void;
    /** Declares that this endpoint's requests replace the source endpoint's, and decodes its responses as U. */
    replaces<S extends object, U extends object>(source: string): FakeReplacement<S, U>;
  }
  /** Translates between a replacing endpoint and the source endpoint it replaces. */
  export interface FakeReplacement<S, U> {
    /** Decodes the selected requests' bodies as T and answers them with an R. */
    using<T extends object, R extends object>(
      request: (request: T) => S,
      response: (request: T, reference: U) => R,
    ): void;
  }
  export interface Egress {
    /** Starts a rule for requests matching a pattern. */
    fake(pattern: string): FakeEgressRule;
  }
}

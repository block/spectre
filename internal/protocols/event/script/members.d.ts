// The event part of the spectre script API. It augments the shared Ingress and Egress interfaces.
declare module "spectre" {
  /** Declares a reference event type that starts work. */
  export interface IngressEventRule {
    /** Starts the candidate's work as an event of type, or of the reference's own type. */
    triggers(type?: string): IngressEventTrigger;
  }
  /** Says what the candidate receives when the reference starts work. */
  export interface IngressEventTrigger {
    /** Maps the reference's payload, of type From, to the candidate's, of type To. */
    payload<From extends object, To extends object>(map: (payload: From) => To): void;
  }
  /** Declares a reference event type that work produces. */
  export interface EgressEventRule {
    /** Lets reference events of this type that no candidate event claims in the match window expire unquarantined. */
    optional(): EgressEventRule;
    /** Pairs events of this type that both sides send, comparing their payloads as T. */
    match<T extends object>(): EgressEventResult;
    /** Pairs the reference's events of this type with the candidate's events of type. */
    pairs(type: string): EgressEventPairing;
  }
  /** Says how the reference's events compare with the candidate's events of another type. */
  export interface EgressEventPairing {
    /** Compares both sides' payloads as T, unchanged. */
    match<T extends object>(): EgressEventResult;
    /** Maps the reference's payload, of type From, to the candidate's, of type To, and compares them. */
    payload<From extends object, To extends object>(map: (payload: From) => To): EgressEventResult;
  }
  /** Says what the candidate's send returns. Without it, the candidate gets the reference's result unchanged. */
  export interface EgressEventResult {
    /** Maps the reference's result, of type From, to the candidate's, of type To. */
    result<From extends object, To extends object>(map: (result: From) => To): void;
  }
  export interface Ingress {
    /** Starts a rule for reference events of type that start work. */
    event(type: string): IngressEventRule;
  }
  export interface Egress {
    /** Starts a rule for reference events of type that work produces. */
    event(type: string): EgressEventRule;
  }
}

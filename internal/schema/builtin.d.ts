// Types of the payloads SPECTRE's protocol servers create themselves. Every schema
// includes them, and schemas cannot redeclare their modules.

declare module "spectre.connect" {
  /** A Connect error code. */
  type Code =
    | "canceled"
    | "unknown"
    | "invalid_argument"
    | "deadline_exceeded"
    | "not_found"
    | "already_exists"
    | "permission_denied"
    | "resource_exhausted"
    | "failed_precondition"
    | "aborted"
    | "out_of_range"
    | "unimplemented"
    | "internal"
    | "unavailable"
    | "data_loss"
    | "unauthenticated";

  /** The JSON body of a failed Connect unary response. */
  interface Error {
    code: Code;
    message?: string;
    details?: ErrorDetail[];
  }

  /** One protobuf message attached to a Connect error. */
  interface ErrorDetail {
    /** The message's fully qualified type name. */
    type: string;
    /** The message as base64-encoded protobuf. */
    value: string;
    debug?: unknown;
  }
}

declare module "spectre.mysql" {
  /** What egress pairs a MySQL statement by. */
  interface Pairing {
    key: Key;
    /** Pairs the first statement of a transaction only with the first of another. */
    opens?: boolean;
    /** Pairs a statement only with statements of the same transaction. */
    transaction?: string;
  }

  /** A statement's database, and its text or the pattern it matched with the values. */
  interface Key {
    database: string;
    statement?: string;
    pattern?: string;
    /** Values as text, with NULL as null. */
    values?: (string | null)[];
  }
}

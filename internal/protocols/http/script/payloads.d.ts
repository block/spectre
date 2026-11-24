// Types of the payloads the HTTP server creates itself. Every schema includes them, and
// schemas cannot redeclare their modules.

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

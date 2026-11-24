// Types of the payloads the MySQL server creates itself. Every schema includes them, and
// schemas cannot redeclare their modules.

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

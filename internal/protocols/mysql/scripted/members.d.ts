// The MySQL part of the spectre script API. It augments the shared Egress interface.
declare module "spectre" {
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
  /** The service's MySQL statements, written with "?" for each value, whatever their comments or spacing. */
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
    /** Returns a candidate statement's rows from its values and claims; a claimed reply answers nothing else. */
    using(combine: (values: SQLValue[], claims: Claims<T>) => R[]): void;
  }
  /** Splits a reference statement's reply into replies to the candidate's statements. */
  export interface EgressMySQLSplit<R extends object, T extends object[]> {
    /** Offers each reference reply's parts; a candidate statement claims the part offered with its values. */
    using(split: (values: SQLValue[], rows: R[], offers: Offers<T>) => void): void;
  }
  export interface Egress {
    /** The service's MySQL statements. */
    mysql: EgressMySQL;
  }
}

// Public typed stub for the virtual "spectre" module provided by Spectre's Sobek runtime.
// Normaliser scripts import this API; the functions are implemented by the host at runtime.
// @ts-check

/**
 * Normalise one value, which is undefined when missing. Return undefined to remove it.
 * @typedef {(value: any) => any} Normaliser
 */

const unavailable = () => {
  throw new Error("the spectre module is provided by the Spectre runtime");
};

/**
 * Type raw HTTP requests matching pattern with an RPC method's messages.
 * pattern is a "<METHOD> /<path>" pattern in [http.ServeMux](https://pkg.go.dev/net/http#hdr-Patterns) syntax.
 */
export function endpoint(/** @type {string} */ pattern, /** @type {string} */ method) {
  unavailable();
}

/**
 * Register a normaliser for a protobuf field.
 * @param {string} target
 * @param {Normaliser} normaliser
 */
export function field(target, normaliser) {
  unavailable();
}

/**
 * Register a normaliser for every occurrence of a protobuf message.
 * @param {string} target
 * @param {Normaliser} normaliser
 */
export function message(target, normaliser) {
  unavailable();
}

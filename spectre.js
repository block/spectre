// Public typed stub for the virtual "spectre" module provided by Spectre's Sobek runtime.
// Comparator scripts import this API; the functions are implemented by the host at runtime.
// @ts-check

/**
 * @callback Comparator
 * @param {*} reference
 * @param {*} candidate
 * @returns {boolean}
 */

const unavailable = () => {
  throw new Error("the spectre module is provided by the Spectre runtime");
};

/**
 * Register a comparator for a protobuf field.
 * @param {string} target
 * @param {Comparator} comparator
 */
export function field(target, comparator) {
  unavailable();
}

/**
 * Register a comparator for every occurrence of a protobuf message.
 * @param {string} target
 * @param {Comparator} comparator
 */
export function message(target, comparator) {
  unavailable();
}

/**
 * Register a comparator for an RPC response.
 * @param {string} target
 * @param {Comparator} comparator
 */
export function rpc(target, comparator) {
  unavailable();
}

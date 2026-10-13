// Package comparison normalises payloads with scripts, then diffs or hashes them.
package comparison

import "crypto/sha256"

// Hash identifies a payload by its identity and normalised content, so
// equivalent payloads share a hash.
type Hash [sha256.Size]byte

// Package limits checks configured limits. // want "package doc comment exceeds 1 lines"
// Line 2.
package limits

// First line.
// Second line.
// Third line.
var withinLimit = 1

// First line. // want "comment exceeds 3 lines"
// Second line.
// Third line.
// Fourth line.
var overLimit = 1

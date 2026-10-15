// Package comments holds comment length cases.
//
// Line 2.
// Line 3.
//
// Line 4.
// Line 5.
// Line 6.
// Line 7.
//
// Line 8.
// Line 9.
// Line 10.
package comments

// Version of SPECTRE.
//
//nolint:gochecknoglobals
var Version = "dev"

// First line.
//
//
// Second line.
var blankLines = 1

// First line.
//go:generate tool
//extern symbol
//export Symbol
//lint:ignore rule reason
// Second line.
var directives = 1

// First line. // want "comment exceeds 2 lines"
//
//go:generate tool
// Second line.
// Third line.
var exclusionsDoNotResetCount = 1

// First line. // want "comment exceeds 2 lines"
// go:generate is described here.
// Third line.
var spacedDirectiveIsProse = 1

//
//go:generate tool
// First line. // want "comment exceeds 2 lines"
// Second line.
// Third line.
var leadingExclusions = 1

/*
 * First line.
 *

 * Second line.
 */
var blockBlanks = 1

/* First line.

 * Second line.
 * Third line. // want "comment exceeds 2 lines" */
var blockTooLong = 1

/* First line.
//go:generate tool
Third line. // want "comment exceeds 2 lines" */
var directiveInsideBlockIsProse = 1

// First line.
// Second line.
var first = 1

// First line.
// Second line.
var second = 1

var trailing = 1 // Trailing.
// First line.
// Second line.
var afterTrailing = 1

// Package comments holds comment length cases.
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

// First line. // want "comment exceeds two lines"
//
//go:generate tool
// Second line.
// Third line.
var exclusionsDoNotResetCount = 1

// First line. // want "comment exceeds two lines"
// go:generate is described here.
// Third line.
var spacedDirectiveIsProse = 1

//
//go:generate tool
// First line. // want "comment exceeds two lines"
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
 * Third line. // want "comment exceeds two lines" */
var blockTooLong = 1

/* First line.
//go:generate tool
Third line. // want "comment exceeds two lines" */
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

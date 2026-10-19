package status

import "os"

// useColor gates ANSI styling of the report. Report sets it from the output
// stream, so it stays false for pipes, files, and when NO_COLOR is set.
var useColor bool //nolint:gochecknoglobals // Output styling is process-wide state set once at startup.

// ANSI SGR sequences used by the report.
const (
	colReset  = "\033[0m"
	colBold   = "\033[1m"
	colDim    = "\033[2m"
	colRed    = "\033[31m"
	colGreen  = "\033[32m"
	colYellow = "\033[33m"
)

// colorEnabled reports whether f is a terminal and NO_COLOR is unset, following
// the https://no-color.org convention.
func colorEnabled(f *os.File) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok { //nolint:forbidigo // NO_COLOR is a cross-tool convention, not service config.
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// paint wraps s in an ANSI sequence when color is enabled.
func paint(code, s string) string {
	if !useColor {
		return s
	}
	return code + s + colReset
}

func bold(s string) string   { return paint(colBold, s) }
func dim(s string) string    { return paint(colDim, s) }
func red(s string) string    { return paint(colRed, s) }
func green(s string) string  { return paint(colGreen, s) }
func yellow(s string) string { return paint(colYellow, s) }

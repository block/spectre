package exact

import "strings"

// withoutComments is the statement with each comment replaced by a space, which is
// what MySQL runs. Executable comments and optimiser hints are kept, as they change it.
func withoutComments(sql string) string {
	var text strings.Builder
	text.Grow(len(sql))
	for i := 0; i < len(sql); {
		switch end, comment := commentAt(sql, i); {
		case comment:
			text.WriteByte(' ')
			i = end
		case strings.IndexByte("'\"`", sql[i]) >= 0:
			quoted := quotedEnd(sql, i)
			text.WriteString(sql[i:quoted])
			i = quoted
		default:
			text.WriteByte(sql[i])
			i++
		}
	}
	return strings.TrimSpace(text.String())
}

// commentAt reports whether a comment starts at i, and where it ends.
func commentAt(sql string, i int) (end int, comment bool) {
	rest := sql[i:]
	switch {
	case strings.HasPrefix(rest, "#"), strings.HasPrefix(rest, "--") && (len(rest) == 2 || rest[2] <= ' '):
		if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
			return i + newline, true
		}
		return len(sql), true
	case strings.HasPrefix(rest, "/*") && !strings.HasPrefix(rest, "/*!") && !strings.HasPrefix(rest, "/*+"):
		if closing := strings.Index(rest[2:], "*/"); closing >= 0 {
			return i + 2 + closing + 2, true
		}
		return len(sql), true
	}
	return i, false
}

// quotedEnd is where the string or identifier quoted at start ends. Quotes are escaped
// by doubling them, and in strings also by a backslash.
func quotedEnd(sql string, start int) int {
	quote := sql[start]
	for i := start + 1; i < len(sql); i++ {
		switch {
		case sql[i] == '\\' && quote != '`':
			i++
		case sql[i] == quote && i+1 < len(sql) && sql[i+1] == quote:
			i++
		case sql[i] == quote:
			return i + 1
		}
	}
	return len(sql)
}

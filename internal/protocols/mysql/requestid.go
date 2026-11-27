package mysql

import (
	"strings"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/engine"
	"github.com/block/spectre/internal/protocols/mysql/normaliser"
)

// sessionCommand reports whether a command only changes session state: it selects a database, or
// holds only SET statements. Neither is paired, so neither needs a request ID.
func sessionCommand(command normaliser.Command) bool {
	if _, selects := normaliser.SelectedDatabase(command); selects {
		return true
	}
	query, ok := command.(normaliser.Query)
	if !ok {
		return false
	}
	for _, statement := range query.Statements {
		if _, set := statement.(*ast.SetStmt); !set {
			return false
		}
	}
	return true
}

// statementRequestID returns the SPECTRE request ID that a statement's comments carry, if any.
// Errors never quote the value, because quarantine reasons are reported outside the mirror.
func statementRequestID(sql string) (Option[engine.RequestID], error) {
	values := []string{}
	for _, comment := range comments(sql) {
		values = append(values, commentRequestIDs(comment)...)
	}
	if len(values) == 0 {
		return None[engine.RequestID](), nil
	}
	for _, value := range values[1:] {
		if value != values[0] {
			return None[engine.RequestID](), errors.New("statement carries conflicting SPECTRE request IDs")
		}
	}
	id, err := typeid.Parse[engine.RequestID](values[0])
	if err != nil {
		return None[engine.RequestID](), errors.New("statement carries a malformed SPECTRE request ID")
	}
	return Some(id), nil
}

// comments returns the text of a statement's comments, skipping its strings and quoted names, so a
// value never poses as a comment. Executable comments and optimiser hints are SQL, not comments.
func comments(sql string) []string {
	found := []string{}
	for at := 0; at < len(sql); {
		rest := sql[at:]
		switch {
		case rest[0] == '\'' || rest[0] == '"' || rest[0] == '`':
			at += quotedLength(rest)
		case rest[0] == '#':
			comment, _, _ := strings.Cut(rest[1:], "\n")
			found = append(found, comment)
			at += 1 + len(comment)
		case strings.HasPrefix(rest, "--") && (len(rest) == 2 || rest[2] <= ' '):
			// MySQL reads -- as a comment only before a space or control character.
			comment, _, _ := strings.Cut(rest[2:], "\n")
			found = append(found, comment)
			at += 2 + len(comment)
		case strings.HasPrefix(rest, "/*"):
			comment, _, closed := strings.Cut(rest[2:], "*/")
			if !strings.HasPrefix(comment, "!") && !strings.HasPrefix(comment, "+") {
				found = append(found, comment)
			}
			at += 2 + len(comment)
			if closed {
				at += 2
			}
		default:
			at++
		}
	}
	return found
}

// quotedLength returns the length of the string or quoted name that text starts with,
// as MySQL reads it without ANSI_QUOTES or NO_BACKSLASH_ESCAPES.
func quotedLength(text string) int {
	quote := text[0]
	for at := 1; at < len(text); at++ {
		switch {
		case text[at] == '\\' && quote != '`':
			at++
		case text[at] == quote && at+1 < len(text) && text[at+1] == quote:
			at++
		case text[at] == quote:
			return at + 1
		}
	}
	return len(text)
}

// commentRequestIDs returns the words of a comment that start with the request ID prefix, written
// alone, in a tag such as key=srid_…, or quoted as sqlcommenter quotes values.
func commentRequestIDs(comment string) []string {
	values := []string{}
	marker := engine.RequestID{}.Prefix() + "_"
	for rest := comment; ; {
		at := strings.Index(rest, marker)
		if at < 0 {
			return values
		}
		end := at + len(marker)
		for end < len(rest) && isWordByte(rest[end]) {
			end++
		}
		// A word that only contains the prefix, such as other_srid_…, is not one.
		if at == 0 || !isWordByte(rest[at-1]) {
			values = append(values, rest[at:end])
		}
		rest = rest[end:]
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

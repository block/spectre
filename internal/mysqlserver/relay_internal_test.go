package mysqlserver

import (
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestKillStatementsNameTheDatabaseThread(t *testing.T) {
	threads := newThreads()
	thread := threads.reserve()
	defer threads.connect(thread, 42)()
	for _, test := range []struct {
		name      string
		statement string
		expected  string
		changed   bool
		err       string
	}{
		{name: "Query", statement: "KILL QUERY 3000000000", expected: "KILL QUERY 42", changed: true},
		{name: "Connection", statement: " kill connection 3000000000 ; ", expected: "KILL connection 42", changed: true},
		{name: "Bare", statement: "KILL 3000000000", expected: "KILL 42", changed: true},
		{name: "DatabaseThread", statement: "KILL QUERY 17", expected: "KILL QUERY 17"},
		{name: "OtherStatement", statement: "SELECT 'KILL 3000000000'", expected: "SELECT 'KILL 3000000000'"},
		{name: "UnknownThread", statement: "KILL 3000000001", err: "ERROR 1094 (HY000): Unknown thread id: 3000000001"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rewritten, changed, err := threads.rewriteKill([]byte(test.statement))
			if test.err != "" {
				assert.EqualError(t, err, test.err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.expected, string(rewritten))
			assert.Equal(t, test.changed, changed)
		})
	}
}

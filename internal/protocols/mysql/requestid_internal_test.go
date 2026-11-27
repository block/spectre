package mysql

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"go.jetify.com/typeid"

	"github.com/block/spectre/internal/engine"
)

func TestStatementsCarryRequestIDsInTheirComments(t *testing.T) {
	id := typeid.Must(typeid.New[engine.RequestID]())
	other := typeid.Must(typeid.New[engine.RequestID]())
	for name, test := range map[string]struct {
		statement string
		expected  Option[engine.RequestID]
		err       string
	}{
		"None":               {statement: "SELECT 1", expected: None[engine.RequestID]()},
		"OtherComment":       {statement: "SELECT 1 /* reference */", expected: None[engine.RequestID]()},
		"Alone":              {statement: "SELECT 1 /* " + id.String() + " */", expected: Some(id)},
		"SqCommon":           {statement: "SELECT 1 -- app/models/order.rb:12 speleo-trace-id=abc spectre_request_id=" + id.String(), expected: Some(id)},
		"SQLCommenter":       {statement: "SELECT 1 /*action='index',spectre_request_id='" + id.String() + "',traceparent='00-1-2-01'*/", expected: Some(id)},
		"Leading":            {statement: "/* " + id.String() + " */ SELECT 1", expected: Some(id)},
		"Inside":             {statement: "SELECT /* " + id.String() + " */ 1", expected: Some(id)},
		"BeforeLineComment":  {statement: "SELECT 1 /* " + id.String() + " */ -- caller", expected: Some(id)},
		"Hash":               {statement: "SELECT 1 # " + id.String(), expected: Some(id)},
		"LineEnds":           {statement: "SELECT 1 -- " + id.String() + "\nFROM dual", expected: Some(id)},
		"SameTwice":          {statement: "SELECT 1 /* " + id.String() + " */; SELECT 2 /* " + id.String() + " */", expected: Some(id)},
		"Conflicting":        {statement: "SELECT 1 /* " + id.String() + " */; SELECT 2 /* " + other.String() + " */", err: "statement carries conflicting SPECTRE request IDs"},
		"Malformed":          {statement: "SELECT 1 /* srid_nonsense */", err: "statement carries a malformed SPECTRE request ID"},
		"Empty":              {statement: "SELECT 1 /* srid_ */", err: "statement carries a malformed SPECTRE request ID"},
		"OtherTypeID":        {statement: "SELECT 1 /* exchange_01jx0000000000000000000000 */", expected: None[engine.RequestID]()},
		"InLongerWord":       {statement: "SELECT 1 /* other_srid_nonsense xsrid_nonsense */", expected: None[engine.RequestID]()},
		"InString":           {statement: "SELECT '/* " + id.String() + " */'", expected: None[engine.RequestID]()},
		"InEscapedString":    {statement: `SELECT 'it\'s /* srid_nonsense */', "a""b -- srid_nonsense"`, expected: None[engine.RequestID]()},
		"InQuotedName":       {statement: "SELECT 1 AS `-- srid_nonsense`", expected: None[engine.RequestID]()},
		"AfterString":        {statement: "SELECT 'a' /* " + id.String() + " */", expected: Some(id)},
		"DashesWithoutSpace": {statement: "SELECT 1--srid_nonsense", expected: None[engine.RequestID]()},
		"ExecutableComment":  {statement: "SELECT 1 /*!80000 srid_nonsense */", expected: None[engine.RequestID]()},
		"OptimiserHint":      {statement: "SELECT /*+ srid_nonsense */ 1", expected: None[engine.RequestID]()},
		"Unclosed":           {statement: "SELECT 1 /* " + id.String(), expected: Some(id)},
	} {
		t.Run(name, func(t *testing.T) {
			named, err := statementRequestID(test.statement)
			if test.err != "" {
				assert.EqualError(t, err, test.err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.expected, named)
		})
	}
}

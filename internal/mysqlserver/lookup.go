package mysqlserver

import (
	"context"

	. "github.com/alecthomas/types/optional"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/protocol"
	"github.com/block/spectre/internal/schema"
)

// Protocol is the protocol of MySQL servers. Its routes name the table a lookup reads.
const Protocol model.Protocol = "mysql"

// lookup is one read of a table's rows by equality on its columns. Egress pairs a
// candidate lookup with the reference lookup of the same table and values.
type lookup struct {
	schema string
	table  string
	where  map[string]string
}

func newLookup(read table, where map[string]string) lookup {
	return lookup{schema: read.schema, table: read.name, where: where}
}

// route names the table read, so lookups of one table share a route.
func (l lookup) route() model.Route {
	return model.Route("SELECT " + l.schema + "." + l.table)
}

// request is the payload egress hashes: the values the lookup's columns equal.
func (l lookup) request(context.Context) (protocol.Message, error) {
	where := make(map[string]any, len(l.where))
	for name, value := range l.where {
		where[name] = value
	}
	return protocol.Message{Type: None[schema.TypeRef](), Value: map[string]any{"where": where}}, nil
}

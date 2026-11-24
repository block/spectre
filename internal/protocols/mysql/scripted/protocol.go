package scripted

import (
	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/schema"
)

// Protocol is the protocol of MySQL servers.
const Protocol model.Protocol = "mysql"

// Pairing is the payload type that egress pairs a MySQL statement by.
const Pairing schema.TypeRef = "spectre.mysql.Pairing"

package mysqlserver

import (
	"encoding/binary"

	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
)

// unknownState is the SQL state of errors that carry none.
const unknownState = "HY000"

// replyPart is the part of a text protocol reply that the next packet holds.
type replyPart int

const (
	firstPacket replyPart = iota
	columnPackets
	columnsEnd
	rowPackets
)

// replyReader reads the packets of one command's reply in the text protocol.
type replyReader struct {
	// deprecateEOF ends result sets with an OK packet instead of EOF packets.
	deprecateEOF bool
	part         replyPart
	remaining    uint64
	rows         normaliser.Rows
	// unofferable is set by replies a normaliser cannot hold, such as several
	// result sets or a local file upload.
	unofferable bool
}

func newReplyReader(capability uint32) *replyReader {
	return &replyReader{deprecateEOF: capability&mysql.CLIENT_DEPRECATE_EOF != 0}
}

// offerable reports whether the reply read so far can be offered to the candidate.
func (r *replyReader) offerable() bool {
	return !r.unofferable
}

// read reads the next packet's payload, and returns the reply once it is complete.
func (r *replyReader) read(payload []byte) (Option[normaliser.Reply], error) {
	if len(payload) == 0 {
		return None[normaliser.Reply](), errors.New("MySQL reply has an empty packet")
	}
	switch r.part {
	case firstPacket:
		return r.first(payload)
	case columnPackets:
		field, err := mysql.FieldData(payload).Parse()
		if err != nil {
			return None[normaliser.Reply](), errors.Wrap(err, "read a MySQL column")
		}
		r.rows.Columns = append(r.rows.Columns, columnOf(field))
		r.remaining--
		if r.remaining == 0 {
			r.part = rowPackets
			if !r.deprecateEOF {
				r.part = columnsEnd
			}
		}
		return None[normaliser.Reply](), nil
	case columnsEnd:
		if !isEOF(payload) {
			return None[normaliser.Reply](), errors.New("MySQL columns do not end with EOF")
		}
		r.part = rowPackets
		return None[normaliser.Reply](), nil
	case rowPackets:
		return r.row(payload)
	}
	return None[normaliser.Reply](), errors.Errorf("unknown reply part %d", r.part)
}

// first reads the packet that says what kind of reply follows.
func (r *replyReader) first(payload []byte) (Option[normaliser.Reply], error) {
	switch payload[0] {
	case mysql.OK_HEADER:
		ok, err := parseOK(payload)
		if err != nil {
			return None[normaliser.Reply](), err
		}
		return r.end(ok, ok.Status)
	case mysql.ERR_HEADER:
		return Some[normaliser.Reply](parseError(payload)), nil
	case mysql.LocalInFile_HEADER:
		// The reference uploads a file, which the candidate cannot.
		r.unofferable = true
		return Some[normaliser.Reply](normaliser.OK{}), nil
	}
	count, _, err := lengthEncodedInt(payload)
	if err != nil || count == 0 {
		return None[normaliser.Reply](), errors.New("MySQL reply has no column count")
	}
	r.part, r.remaining, r.rows = columnPackets, count, normaliser.Rows{}
	return None[normaliser.Reply](), nil
}

// row reads a row, or the packet that ends the rows.
func (r *replyReader) row(payload []byte) (Option[normaliser.Reply], error) {
	switch {
	case payload[0] == mysql.ERR_HEADER:
		return Some[normaliser.Reply](parseError(payload)), nil
	case r.deprecateEOF && payload[0] == mysql.EOF_HEADER && len(payload) < maxPayload:
		ok, err := parseOK(payload)
		if err != nil {
			return None[normaliser.Reply](), err
		}
		return r.end(r.rows, ok.Status)
	case !r.deprecateEOF && isEOF(payload):
		return r.end(r.rows, binary.LittleEndian.Uint16(payload[3:]))
	}
	values := make([]normaliser.Value, 0, len(r.rows.Columns))
	rest := payload
	for range r.rows.Columns {
		if len(rest) > 0 && rest[0] == 0xfb {
			values, rest = append(values, None[string]()), rest[1:]
			continue
		}
		value, after, err := lengthEncoded(rest)
		if err != nil {
			return None[normaliser.Reply](), errors.Wrap(err, "read a MySQL row")
		}
		values, rest = append(values, Some(string(value))), after
	}
	r.rows.Rows = append(r.rows.Rows, values)
	return None[normaliser.Reply](), nil
}

// end finishes a result with status, unless the database says more results follow.
func (r *replyReader) end(reply normaliser.Reply, status uint16) (Option[normaliser.Reply], error) {
	if status&mysql.SERVER_MORE_RESULTS_EXISTS == 0 {
		return Some(reply), nil
	}
	r.unofferable = true
	r.part, r.rows = firstPacket, normaliser.Rows{}
	return None[normaliser.Reply](), nil
}

// isEOF reports whether payload is an EOF packet, which is shorter than any row.
func isEOF(payload []byte) bool {
	return payload[0] == mysql.EOF_HEADER && len(payload) == 5
}

// parseOK reads an OK packet.
func parseOK(payload []byte) (normaliser.OK, error) {
	affected, rest, err := lengthEncodedInt(payload[1:])
	if err != nil {
		return normaliser.OK{}, errors.Wrap(err, "read the affected rows")
	}
	insertID, rest, err := lengthEncodedInt(rest)
	if err != nil {
		return normaliser.OK{}, errors.Wrap(err, "read the insert ID")
	}
	if len(rest) < 4 {
		return normaliser.OK{}, errors.New("MySQL OK packet has no status")
	}
	return normaliser.OK{
		AffectedRows: affected, InsertID: insertID,
		Status: binary.LittleEndian.Uint16(rest), Warnings: binary.LittleEndian.Uint16(rest[2:]),
	}, nil
}

// parseError reads an ERR packet, which has a state after protocol 4.1 logins.
func parseError(payload []byte) normaliser.Error {
	if len(payload) < 3 {
		return normaliser.Error{Code: mysql.ER_UNKNOWN_ERROR, State: unknownState, Message: "malformed error"}
	}
	reply := normaliser.Error{Code: binary.LittleEndian.Uint16(payload[1:]), State: unknownState}
	rest := payload[3:]
	if len(rest) >= 6 && rest[0] == '#' {
		reply.State, rest = string(rest[1:6]), rest[6:]
	}
	reply.Message = string(rest)
	return reply
}

// columnOf copies a column definition, so it outlives the packet it was read from.
func columnOf(field *mysql.Field) normaliser.Column {
	return normaliser.Column{
		Schema: string(field.Schema), Table: string(field.Table), OrgTable: string(field.OrgTable),
		Name: string(field.Name), OrgName: string(field.OrgName), Charset: field.Charset,
		Length: field.ColumnLength, Type: field.Type, Flags: field.Flag, Decimals: field.Decimal,
	}
}

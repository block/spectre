package mysqlserver

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/alecthomas/assert/v2"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/model"
	"github.com/block/spectre/internal/mysqlserver/exact"
	"github.com/block/spectre/internal/mysqlserver/normaliser"
	"github.com/block/spectre/internal/proxy"
)

var items = normaliser.Rows{
	Columns: []normaliser.Column{
		{Schema: "shop", Table: "i", OrgTable: "items", Name: "id", OrgName: "id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: mysql.PRI_KEY_FLAG},
		{Schema: "shop", Table: "i", OrgTable: "items", Name: "sku", OrgName: "sku", Charset: 255, Length: 128, Type: mysql.MYSQL_TYPE_VAR_STRING},
	},
	Rows: [][]normaliser.Value{{Some("1"), Some("apple")}, {Some("2"), None[string]()}, {Some(""), Some("")}},
}

func TestRepliesAreReadFromTheirPackets(t *testing.T) {
	for _, test := range []struct {
		name  string
		reply normaliser.Reply
	}{
		{name: "Rows", reply: items},
		{name: "NoRows", reply: normaliser.Rows{Columns: items.Columns}},
		{name: "OK", reply: normaliser.OK{AffectedRows: 2, InsertID: 300, Status: mysql.SERVER_STATUS_IN_TRANS, Warnings: 1}},
		{name: "Error", reply: normaliser.Error{Code: 1146, State: "42S02", Message: "Table 'shop.missing' doesn't exist"}},
	} {
		for _, deprecateEOF := range []bool{false, true} {
			t.Run(test.name, func(t *testing.T) {
				reader := readerOf(deprecateEOF)
				assert.Equal(t, Some(test.reply), readByteByByte(t, reader, packetsOf(test.reply, 0, deprecateEOF)))
				assert.True(t, reader.offerable())
				if rows, ok := test.reply.(normaliser.Rows); ok {
					// Reading holds the budget that saving the reply takes over.
					assert.Equal(t, replySize(rows), reader.size())
				}
			})
		}
	}
}

func TestRepliesWithMoreResultsAreNotOffered(t *testing.T) {
	for _, deprecateEOF := range []bool{false, true} {
		reader := readerOf(deprecateEOF)
		stream := append(packetsOf(items, mysql.SERVER_MORE_RESULTS_EXISTS, deprecateEOF), packetsOf(normaliser.OK{}, 0, deprecateEOF)...)
		assert.Equal(t, Some[normaliser.Reply](normaliser.OK{}), readByteByByte(t, reader, stream))
		assert.False(t, reader.offerable())
	}
}

func TestLocalFileUploadsAreNotOffered(t *testing.T) {
	reader := readerOf(true)
	reply := readByteByByte(t, reader, frame(1, append([]byte{mysql.LocalInFile_HEADER}, "/tmp/items.csv"...)))
	assert.True(t, reply.Ok())
	assert.False(t, reader.offerable())
}

func TestErrorsAmongRowsEndTheReply(t *testing.T) {
	failure := normaliser.Error{Code: mysql.ER_QUERY_INTERRUPTED, State: "70100", Message: "Query execution was interrupted"}
	packets := packetsOf(items, 0, true)
	stream := append(packets[:bytes.Index(packets, frame(0, row(items.Rows[1])))], packetsOf(failure, 0, true)...)
	assert.Equal(t, Some[normaliser.Reply](failure), readByteByByte(t, readerOf(true), stream))
}

func TestMalformedRepliesFail(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "NoColumns", payload: []byte{0}},
		{name: "OKWithoutStatus", payload: []byte{mysql.OK_HEADER, 0, 0}},
		{name: "TruncatedColumnCount", payload: []byte{0xfc, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readerOf(true).read(test.payload)
			assert.Error(t, err)
		})
	}
}

func TestTruncatedRowsFail(t *testing.T) {
	reader := readerOf(true)
	framer := newFramer()
	framer.write(packetsOf(normaliser.Rows{Columns: items.Columns}, 0, true))
	for range 1 + len(items.Columns) {
		payload, ok := framer.next()
		assert.True(t, ok)
		_, err := reader.read(payload)
		assert.NoError(t, err)
	}
	_, err := reader.read([]byte{5, 'a'})
	assert.Error(t, err)
}

func TestLongPayloadsContinueInTheNextPacket(t *testing.T) {
	long := bytes.Repeat([]byte{'x'}, maxPayload+3)
	framer := newFramer()
	framer.write(append(append(frame(0, long[:maxPayload]), frame(1, long[maxPayload:])...), frame(2, []byte("next"))...))
	payload, ok := framer.next()
	assert.True(t, ok)
	assert.Equal(t, long, payload)
	payload, ok = framer.next()
	assert.True(t, ok)
	assert.Equal(t, []byte("next"), payload)
	_, ok = framer.next()
	assert.False(t, ok)
}

func TestRepliesBeyondTheBudgetQuarantineAndReturnIt(t *testing.T) {
	server, budget := budgetedServer()
	observer := newObserver(t.Context(), server, exact.New().NewSession(), mysql.CLIENT_PROTOCOL_41|mysql.CLIENT_DEPRECATE_EOF)
	long := normaliser.Rows{Columns: items.Columns}
	for range 20 {
		long.Rows = append(long.Rows, items.Rows[0])
	}

	observer.sent(append([]byte{mysql.COM_QUERY}, "SELECT id, sku FROM items"...), true)
	for _, b := range packetsOf(long, 0, true) {
		observer.received([]byte{b})()
	}
	quarantine, quarantined := server.candidates.QuarantineStatus().Get()
	assert.True(t, quarantined)
	assert.Equal(t, model.QuarantineReason{
		Kind:    model.QuarantineCapacity,
		Traffic: Some(model.Traffic{Direction: model.Egress, Protocol: Protocol, Route: "SELECT"}),
		Detail:  "saved reference replies exceed the MySQL saved replies size",
	}, quarantine.Reason)
	assert.True(t, budget.Reserve(budget.Limit()))
}

func TestRepliesBeingReadHoldTheBudgetUntilTheConnectionEnds(t *testing.T) {
	server, budget := budgetedServer()
	observer := newObserver(t.Context(), server, exact.New().NewSession(), mysql.CLIENT_PROTOCOL_41|mysql.CLIENT_DEPRECATE_EOF)
	packets := packetsOf(items, 0, true)

	observer.sent(append([]byte{mysql.COM_QUERY}, "SELECT id, sku FROM items"...), true)
	observer.received(packets[:len(packets)-12])()
	assert.False(t, budget.Reserve(budget.Limit()))
	observer.close()
	assert.True(t, budget.Reserve(budget.Limit()))
}

func TestSavedRepliesHoldTheirSizeUntilReleased(t *testing.T) {
	budget := proxy.NewBudget(replyBytes + rowBytes + valueBytes + len("apple"))
	saved, reserved := reserveReply(budget, normaliser.Rows{Rows: [][]normaliser.Value{{Some("apple")}}})
	assert.True(t, reserved)
	_, reserved = reserveReply(budget, normaliser.OK{})
	assert.False(t, reserved)
	saved.Release()
	saved.Release()
	_, reserved = reserveReply(budget, normaliser.OK{})
	assert.True(t, reserved)
}

// budgetedServer is a server with room to save one reply of items, and no engine.
func budgetedServer() (*Server, *proxy.Budget) {
	budget := proxy.NewBudget(replySize(items) + 100)
	return &Server{budget: budget, candidates: proxy.NewCandidates(proxy.CandidatesConfig{CandidateMaxInFlight: 1})}, budget
}

func readerOf(deprecateEOF bool) *replyReader {
	if deprecateEOF {
		return newReplyReader(mysql.CLIENT_PROTOCOL_41 | mysql.CLIENT_DEPRECATE_EOF)
	}
	return newReplyReader(mysql.CLIENT_PROTOCOL_41)
}

// readByteByByte reads stream one byte at a time, as the slowest database would send it.
func readByteByByte(t *testing.T, reader *replyReader, stream []byte) Option[normaliser.Reply] {
	t.Helper()
	framer := newFramer()
	for _, b := range stream {
		framer.write([]byte{b})
		for {
			payload, ok := framer.next()
			if !ok {
				break
			}
			reply, err := reader.read(payload)
			assert.NoError(t, err)
			if reply.Ok() {
				return reply
			}
		}
	}
	return None[normaliser.Reply]()
}

// packetsOf encodes reply as a database sends it, ending with status.
func packetsOf(reply normaliser.Reply, status uint16, deprecateEOF bool) []byte {
	switch reply := reply.(type) {
	case normaliser.Rows:
		resultset := resultsetOf(reply)
		stream := frame(1, mysql.PutLengthEncodedInt(uint64(len(reply.Columns))))
		for _, field := range resultset.Fields {
			stream = append(stream, frame(0, field.Dump())...)
		}
		if !deprecateEOF {
			stream = append(stream, frame(0, eof(0))...)
		}
		for _, data := range resultset.RowDatas {
			stream = append(stream, frame(0, data)...)
		}
		if deprecateEOF {
			return append(stream, frame(0, append([]byte{mysql.EOF_HEADER}, ok(0, 0, status, 0)[1:]...))...)
		}
		return append(stream, frame(0, eof(status))...)
	case normaliser.OK:
		return frame(1, ok(reply.AffectedRows, reply.InsertID, reply.Status|status, reply.Warnings))
	case normaliser.Error:
		return frame(1, errorPayload(&mysql.MyError{Code: reply.Code, State: reply.State, Message: reply.Message}))
	}
	panic("unknown reply")
}

// row encodes values in the text protocol.
func row(values []normaliser.Value) []byte {
	return resultsetOf(normaliser.Rows{Rows: [][]normaliser.Value{values}}).RowDatas[0]
}

func ok(affected, insertID uint64, status, warnings uint16) []byte {
	payload := append([]byte{mysql.OK_HEADER}, mysql.PutLengthEncodedInt(affected)...)
	payload = append(payload, mysql.PutLengthEncodedInt(insertID)...)
	payload = binary.LittleEndian.AppendUint16(payload, status)
	return binary.LittleEndian.AppendUint16(payload, warnings)
}

func eof(status uint16) []byte {
	return binary.LittleEndian.AppendUint16([]byte{mysql.EOF_HEADER, 0, 0}, status)
}

// frame puts payload in one packet. Sequence numbers are not checked by readers.
func frame(sequence uint8, payload []byte) []byte {
	header := binary.LittleEndian.AppendUint32(nil, uint32(len(payload))|uint32(sequence)<<24)
	return append(header, payload...)
}

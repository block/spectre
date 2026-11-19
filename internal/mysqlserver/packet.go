package mysqlserver

import (
	"encoding/binary"
	"io"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/mysql"
)

// maxPayload is the largest payload of one packet. Longer ones continue in the next.
const maxPayload = 1<<24 - 1

// packet is one MySQL protocol packet.
type packet struct {
	sequence uint8
	payload  []byte
}

// readPacket reads one packet from r.
func readPacket(r io.Reader) (packet, error) {
	header := [4]byte{}
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return packet{}, errors.WithStack(err)
	}
	payload := make([]byte, int(header[0])|int(header[1])<<8|int(header[2])<<16)
	if _, err := io.ReadFull(r, payload); err != nil {
		return packet{}, errors.WithStack(err)
	}
	return packet{sequence: header[3], payload: payload}, nil
}

// writePacket writes one packet to w, whose payload must fit in one packet.
func writePacket(w io.Writer, sequence uint8, payload []byte) error {
	if len(payload) > maxPayload {
		return errors.Errorf("MySQL packet payload of %d bytes is too long", len(payload))
	}
	header := binary.LittleEndian.AppendUint32(nil, uint32(len(payload))|uint32(sequence)<<24) //nolint:gosec // The length fits in 24 bits.
	_, err := w.Write(append(header, payload...))
	return errors.WithStack(err)
}

// errorPayload is the payload of an ERR packet for err, which keeps the database's
// code and state when it is the database's error.
func errorPayload(err error) []byte {
	reported, ok := errors.AsType[*mysql.MyError](err)
	if !ok {
		reported = mysql.NewError(mysql.ER_UNKNOWN_ERROR, err.Error())
	}
	payload := binary.LittleEndian.AppendUint16([]byte{mysql.ERR_HEADER}, reported.Code)
	payload = append(payload, '#')
	payload = append(payload, (reported.State + "     ")[:5]...)
	return append(payload, reported.Message...)
}

// okPayload is the payload of an OK packet that changed nothing.
func okPayload(capability uint32) []byte {
	payload := []byte{mysql.OK_HEADER, 0, 0}
	payload = binary.LittleEndian.AppendUint16(payload, mysql.SERVER_STATUS_AUTOCOMMIT)
	payload = binary.LittleEndian.AppendUint16(payload, 0)
	if capability&mysql.CLIENT_SESSION_TRACK != 0 {
		// An empty information string, encoded by length.
		payload = append(payload, 0)
	}
	return payload
}

// framer splits a stream of packets into payloads, joining those that continue in
// the next packet.
type framer struct {
	buffered []byte
	partial  []byte
}

func newFramer() *framer {
	return &framer{}
}

// write adds data from the stream.
func (f *framer) write(data []byte) {
	f.buffered = append(f.buffered, data...)
}

// pending is how many bytes are held for payloads that have not arrived in full.
func (f *framer) pending() int {
	return len(f.buffered) + len(f.partial)
}

// next returns the next whole payload, once all of it has arrived.
func (f *framer) next() (payload []byte, ok bool) {
	for len(f.buffered) >= 4 {
		length := int(f.buffered[0]) | int(f.buffered[1])<<8 | int(f.buffered[2])<<16
		if len(f.buffered) < 4+length {
			return nil, false
		}
		f.partial = append(f.partial, f.buffered[4:4+length]...)
		f.buffered = f.buffered[4+length:]
		if length < maxPayload {
			payload, f.partial = f.partial, nil
			return payload, true
		}
	}
	return nil, false
}

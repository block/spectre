package mysqlserver

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"

	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/mysql"
)

// requiredCapabilities are the handshake features that the reference listener offers,
// none of which change later packets.
const requiredCapabilities = mysql.CLIENT_PROTOCOL_41 | mysql.CLIENT_SECURE_CONNECTION | mysql.CLIENT_PLUGIN_AUTH |
	mysql.CLIENT_PLUGIN_AUTH_LENENC_CLIENT_DATA | mysql.CLIENT_LONG_PASSWORD | mysql.CLIENT_LONG_FLAG |
	mysql.CLIENT_TRANSACTIONS | mysql.CLIENT_CONNECT_WITH_DB | mysql.CLIENT_CONNECT_ATTRS

// passedCapabilities are the features the reference may choose, which the database
// connection then uses too, so the packets between them mean the same on both.
const passedCapabilities = mysql.CLIENT_FOUND_ROWS | mysql.CLIENT_IGNORE_SPACE | mysql.CLIENT_MULTI_STATEMENTS |
	mysql.CLIENT_MULTI_RESULTS | mysql.CLIENT_PS_MULTI_RESULTS | mysql.CLIENT_LOCAL_FILES |
	mysql.CLIENT_SESSION_TRACK | mysql.CLIENT_DEPRECATE_EOF

// heldBackCapabilities are never offered, so neither connection uses them.
const heldBackCapabilities = mysql.CLIENT_QUERY_ATTRIBUTES

// login is what a reference asked for while connecting.
type login struct {
	// capability is the features both sides support.
	capability uint32
	collation  uint8
	database   string
	attributes map[string]string
	// sequence is the sequence number of the packet that answers the login.
	sequence uint8
}

// greet sends the reference the handshake of a server with version and the
// connection's thread, and reads its login with any credentials.
func greet(conn io.Writer, from io.Reader, version string, thread uint32) (login, error) {
	salt, err := newSalt()
	if err != nil {
		return login{}, err
	}
	if err := writePacket(conn, 0, greeting(version, thread, salt)); err != nil {
		return login{}, err
	}
	response, err := readPacket(from)
	if err != nil {
		return login{}, err
	}
	parsed, plugin, err := parseLogin(response.payload)
	if err != nil {
		return login{}, err
	}
	parsed.sequence = response.sequence + 1
	if plugin == "" || plugin == mysql.AUTH_NATIVE_PASSWORD {
		return parsed, nil
	}
	// Any credentials are accepted, but the client must still answer the password plugin.
	switchRequest := append(append([]byte{mysql.EOF_HEADER}, mysql.AUTH_NATIVE_PASSWORD+"\x00"...), salt...)
	if err := writePacket(conn, parsed.sequence, append(switchRequest, 0)); err != nil {
		return login{}, err
	}
	answer, err := readPacket(from)
	if err != nil {
		return login{}, err
	}
	parsed.sequence = answer.sequence + 1
	return parsed, nil
}

// greeting is the server's handshake, which offers native passwords and no TLS.
func greeting(version string, thread uint32, salt []byte) []byte {
	offered := requiredCapabilities | passedCapabilities
	payload := append([]byte{10}, version+"\x00"...)
	payload = binary.LittleEndian.AppendUint32(payload, thread)
	payload = append(append(payload, salt[:8]...), 0)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(offered))
	payload = append(payload, mysql.DEFAULT_COLLATION_ID)
	payload = binary.LittleEndian.AppendUint16(payload, mysql.SERVER_STATUS_AUTOCOMMIT)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(offered>>16))
	payload = append(payload, byte(len(salt)+1))
	payload = append(payload, make([]byte, 10)...)
	payload = append(append(payload, salt[8:]...), 0)
	return append(payload, mysql.AUTH_NATIVE_PASSWORD+"\x00"...)
}

// newSalt returns a password salt of printable characters, as clients expect.
func newSalt() ([]byte, error) {
	salt := make([]byte, 20)
	if _, err := rand.Read(salt); err != nil {
		return nil, errors.Wrap(err, "generate a MySQL salt")
	}
	for i := range salt {
		salt[i] = '!' + salt[i]%('~'-'!')
	}
	return salt, nil
}

// parseLogin reads a handshake response, and returns its password plugin.
func parseLogin(payload []byte) (parsed login, plugin string, err error) {
	if len(payload) < 32 {
		return login{}, "", errors.New("MySQL login is too short")
	}
	requested := binary.LittleEndian.Uint32(payload)
	if len(payload) == 32 && requested&mysql.CLIENT_SSL != 0 {
		return login{}, "", errors.New("MySQL client asked for TLS, which the reference listener does not offer")
	}
	parsed = login{capability: requested & (requiredCapabilities | passedCapabilities), collation: payload[8]}
	rest := payload[32:]
	if _, rest, err = nulTerminated(rest); err != nil {
		return login{}, "", errors.Wrap(err, "read the MySQL user")
	}
	if rest, err = skipAuthData(rest, requested); err != nil {
		return login{}, "", err
	}
	if requested&mysql.CLIENT_CONNECT_WITH_DB != 0 && len(rest) > 0 {
		if parsed.database, rest, err = nulTerminated(rest); err != nil {
			return login{}, "", errors.Wrap(err, "read the MySQL database")
		}
	}
	if requested&mysql.CLIENT_PLUGIN_AUTH != 0 && len(rest) > 0 {
		if plugin, rest, err = nulTerminated(rest); err != nil {
			return login{}, "", errors.Wrap(err, "read the MySQL password plugin")
		}
	}
	if requested&mysql.CLIENT_CONNECT_ATTRS != 0 && len(rest) > 0 {
		if parsed.attributes, err = parseAttributes(rest); err != nil {
			return login{}, "", err
		}
	}
	return parsed, plugin, nil
}

// skipAuthData skips the password answer, which is never checked.
func skipAuthData(rest []byte, requested uint32) ([]byte, error) {
	switch {
	case requested&mysql.CLIENT_PLUGIN_AUTH_LENENC_CLIENT_DATA != 0:
		length, _, read := mysql.LengthEncodedInt(rest)
		if read == 0 || uint64(len(rest)-read) < length {
			return nil, errors.New("MySQL login has a truncated password")
		}
		return rest[read+int(length):], nil //nolint:gosec // The length is within rest.
	case requested&mysql.CLIENT_SECURE_CONNECTION != 0:
		if len(rest) == 0 || len(rest)-1 < int(rest[0]) {
			return nil, errors.New("MySQL login has a truncated password")
		}
		return rest[1+int(rest[0]):], nil
	default:
		_, after, err := nulTerminated(rest)
		return after, errors.Wrap(err, "read the MySQL password")
	}
}

// parseAttributes reads the connection attributes, such as the client's name.
func parseAttributes(rest []byte) (map[string]string, error) {
	length, _, read := mysql.LengthEncodedInt(rest)
	if read == 0 || uint64(len(rest)-read) < length {
		return nil, errors.New("MySQL login has truncated attributes")
	}
	encoded := rest[read : read+int(length)] //nolint:gosec // The length is within rest.
	attributes := map[string]string{}
	for len(encoded) > 0 {
		key, _, keyLength, err := mysql.LengthEncodedString(encoded)
		if err != nil {
			return nil, errors.Wrap(err, "read a MySQL attribute")
		}
		value, _, valueLength, err := mysql.LengthEncodedString(encoded[keyLength:])
		if err != nil {
			return nil, errors.Wrap(err, "read a MySQL attribute")
		}
		attributes[string(key)] = string(value)
		encoded = encoded[keyLength+valueLength:]
	}
	return attributes, nil
}

// nulTerminated splits off a string that ends with a zero byte.
func nulTerminated(data []byte) (value string, rest []byte, err error) {
	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return "", nil, errors.New("string is not terminated")
	}
	return string(data[:end]), data[end+1:], nil
}

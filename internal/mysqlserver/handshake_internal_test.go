package mysqlserver

import (
	"encoding/binary"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/go-mysql-org/go-mysql/mysql"
)

// loginPrefix is a login's fields before the user, asking for capability.
func loginPrefix(capability uint32) []byte {
	return append(binary.LittleEndian.AppendUint32(nil, capability), make([]byte, 28)...)
}

func TestLoginsWithTruncatedLengthsAreRefused(t *testing.T) {
	lengthEncodedAuth := requiredCapabilities
	for _, test := range []struct {
		name string
		rest []byte
		err  string
	}{
		{name: "TwoByteLength", rest: []byte("app\x00\xfc"), err: "read the MySQL password: length is truncated"},
		{name: "ThreeByteLength", rest: []byte("app\x00\xfd\x01"), err: "read the MySQL password: length is truncated"},
		{name: "EightByteLength", rest: []byte("app\x00\xfe\x01\x02"), err: "read the MySQL password: length is truncated"},
		{name: "HugeLength", rest: []byte("app\x00\xfe\xff\xff\xff\xff\xff\xff\xff\xff"), err: "read the MySQL password: 18446744073709551615 bytes are announced where 0 remain"},
		{name: "PasswordBeyondPacket", rest: []byte("app\x00\x05ab"), err: "read the MySQL password: 5 bytes are announced where 2 remain"},
		{name: "AttributesTruncated", rest: []byte("app\x00\x00db\x00mysql_native_password\x00\xfc"), err: "read the MySQL attributes: length is truncated"},
		{name: "AttributeValueTruncated", rest: []byte("app\x00\x00db\x00mysql_native_password\x00\x03\x01k\xfd"), err: "read a MySQL attribute: length is truncated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseLogin(append(loginPrefix(lengthEncodedAuth), test.rest...))
			assert.EqualError(t, err, test.err)
		})
	}
}

func TestLoginsAreParsed(t *testing.T) {
	payload := append(loginPrefix(requiredCapabilities|mysql.CLIENT_FOUND_ROWS),
		[]byte("app\x00\x02pwdb\x00caching_sha2_password\x00\x0b\x04name\x05value")...)
	parsed, plugin, err := parseLogin(payload)
	assert.NoError(t, err)
	assert.Equal(t, "caching_sha2_password", plugin)
	assert.Equal(t, login{
		capability: requiredCapabilities | mysql.CLIENT_FOUND_ROWS,
		database:   "db",
		attributes: map[string]string{"name": "value"},
	}, parsed)
}

func FuzzLoginsNeverPanic(f *testing.F) {
	f.Add([]byte("app\x00\xfc"))
	f.Add([]byte("app\x00\x02pwdb\x00mysql_native_password\x00\x0b\x04name\x05value"))
	f.Fuzz(func(_ *testing.T, rest []byte) {
		_, _, _ = parseLogin(append(loginPrefix(requiredCapabilities), rest...))
	})
}

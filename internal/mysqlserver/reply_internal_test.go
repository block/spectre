package mysqlserver

import (
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	. "github.com/alecthomas/types/optional"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
	"github.com/block/spectre/internal/proxy"
)

func TestRepliesSurviveConversion(t *testing.T) {
	for _, test := range []struct {
		name  string
		reply normaliser.Reply
	}{
		{name: "Rows", reply: normaliser.Rows{
			Columns: []normaliser.Column{
				{Schema: "shop", Table: "i", OrgTable: "items", Name: "id", OrgName: "id", Charset: 63, Length: 20, Type: mysql.MYSQL_TYPE_LONGLONG, Flags: mysql.PRI_KEY_FLAG},
				{Schema: "shop", Table: "i", OrgTable: "items", Name: "sku", OrgName: "sku", Charset: 255, Length: 128, Type: mysql.MYSQL_TYPE_VAR_STRING},
			},
			Rows: [][]normaliser.Value{{Some("1"), Some("apple")}, {Some("2"), None[string]()}},
		}},
		{name: "NoRows", reply: normaliser.Rows{Columns: []normaliser.Column{{Name: "id"}}, Rows: [][]normaliser.Value{}}},
		{name: "OK", reply: normaliser.OK{AffectedRows: 2, InsertID: 9, Status: mysql.SERVER_STATUS_IN_TRANS, Warnings: 1}},
		{name: "Error", reply: normaliser.Error{Code: 1146, State: "42S02", Message: "Table 'shop.missing' doesn't exist"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := resultOf(test.reply)
			if err != nil {
				converted, answered := errorReply(err)
				assert.True(t, answered)
				assert.Equal(t, test.reply, converted)
				return
			}
			converted, answered := resultReply(result)
			assert.True(t, answered)
			assert.Equal(t, test.reply, converted)
		})
	}
}

func TestFailuresThatAreNotTheDatabasesAnswerAreNotReplies(t *testing.T) {
	_, answered := errorReply(errors.New("connection reset"))
	assert.False(t, answered)
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

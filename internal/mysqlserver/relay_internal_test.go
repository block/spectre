package mysqlserver

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/alecthomas/errors"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/block/spectre/internal/mysqlserver/normaliser"
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

// A slow script splits the reference's first reply; the reference's next statement still
// gets its reply at once, and the session sees both statements in order.
func TestRepliesReachTheReferenceWhileEarlierOnesAreOffered(t *testing.T) {
	server, _ := budgetedServer()
	session := newHeldSession()
	referenceSide, reference := net.Pipe()
	upstream, database := net.Pipe()
	relayed := make(chan struct{})
	go func() {
		defer close(relayed)
		observer := newObserver(t.Context(), server, session, mysql.CLIENT_PROTOCOL_41|mysql.CLIENT_DEPRECATE_EOF)
		relay(t.Context(), newThreads(), observer, referenceSide, referenceSide, upstream)
	}()
	// A broken relay fails the test instead of hanging it.
	deadline := time.Now().Add(10 * time.Second)
	assert.NoError(t, reference.SetDeadline(deadline))
	assert.NoError(t, database.SetDeadline(deadline))

	for _, statement := range []string{"SELECT id, sku FROM items", "SELECT id FROM items"} {
		query := frame(0, append([]byte{mysql.COM_QUERY}, statement...))
		go func() { _, _ = reference.Write(query) }()
		_, err := io.ReadFull(database, make([]byte, len(query)))
		assert.NoError(t, err)
		reply := packetsOf(items, 0, true)
		go func() { _, _ = database.Write(reply) }()
		received := make([]byte, len(reply))
		_, err = io.ReadFull(reference, received)
		assert.NoError(t, err)
		assert.Equal(t, reply, received)
	}
	close(session.release)

	assert.Equal(t, "SELECT id, sku FROM items", <-session.seen)
	assert.Equal(t, "SELECT id FROM items", <-session.seen)
	_ = reference.Close()
	_ = database.Close()
	<-relayed
}

// heldSession holds every reference reply until released, as a slow script would.
type heldSession struct {
	seen    chan string
	release chan struct{}
}

func newHeldSession() *heldSession {
	return &heldSession{seen: make(chan string, 2), release: make(chan struct{})}
}

func (h *heldSession) Reference(_ context.Context, command normaliser.Command, _ normaliser.Reply) ([]normaliser.Unit, error) {
	<-h.release
	if query, ok := command.(normaliser.Query); ok {
		h.seen <- query.SQL
	}
	return nil, nil
}

func (h *heldSession) Candidate(context.Context, normaliser.Command, normaliser.Claim) (normaliser.Reply, error) { //nolint:ireturn // Replies are a sum type.
	return nil, errors.New("held sessions only follow the reference")
}

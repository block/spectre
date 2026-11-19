package mysqlserver

import (
	"context"
	"testing"

	"github.com/alecthomas/assert/v2"
)

func TestSecretCredentialsAreReadAgainOnlyWhenStale(t *testing.T) {
	values := []string{`{"username": "app", "password": "one"}`, `{"username": "app", "password": "two"}`}
	reads := 0
	source := newSecretCredentials(func(_ context.Context) (string, error) {
		reads++
		return values[reads-1], nil
	})

	first, err := source.get(t.Context(), false)
	assert.NoError(t, err)
	cached, err := source.get(t.Context(), false)
	assert.NoError(t, err)
	rotated, err := source.get(t.Context(), true)
	assert.NoError(t, err)
	assert.Equal(t, []credentials{{user: "app", password: "one"}, {user: "app", password: "one"}, {user: "app", password: "two"}},
		[]credentials{first, cached, rotated})
	assert.Equal(t, 2, reads)
}

func TestSecretCredentialsNeedAUsernameAndPassword(t *testing.T) {
	source := newSecretCredentials(func(_ context.Context) (string, error) {
		return `{"username": "app"}`, nil
	})

	_, err := source.get(t.Context(), false)
	assert.EqualError(t, err, "MySQL secret has no username or password")
}

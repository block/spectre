package contain_test

import (
	"testing"

	"github.com/alecthomas/assert/v2"

	"github.com/block/spectre/internal/contain"
)

func TestApplyRefusesRoot(t *testing.T) {
	err := contain.Apply(t.Context(), contain.Config{UID: 0})
	assert.EqualError(t, err, "uid must not be 0, which would contain every process running as root")
}

func TestRedirectParses(t *testing.T) {
	for _, test := range []struct {
		name     string
		text     string
		expected contain.Redirect
		err      string
	}{
		{name: "Valid", text: "1002:3306=50071", expected: contain.Redirect{UID: 1002, Port: 3306, LocalPort: 50071}},
		{name: "NoLocalPort", text: "1002:3306", err: `redirect "1002:3306" is not UID:PORT=LOCAL_PORT`},
		{name: "NoUser", text: "3306=50071", err: `redirect "3306=50071" is not UID:PORT=LOCAL_PORT`},
		{name: "InvalidUser", text: "app:3306=50071", err: `redirect "app:3306=50071" has an invalid user ID`},
		{name: "ZeroPort", text: "1002:0=50071", err: `redirect "1002:0=50071" has a port that is not between 1 and 65535`},
		{name: "LargePort", text: "1002:3306=65536", err: `redirect "1002:3306=65536" has a port that is not between 1 and 65535`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var redirect contain.Redirect
			err := redirect.UnmarshalText([]byte(test.text))
			if test.err != "" {
				assert.EqualError(t, err, test.err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, test.expected, redirect)
		})
	}
}

func TestApplyRefusesInvalidRedirects(t *testing.T) {
	for _, test := range []struct {
		name      string
		redirects []contain.Redirect
		err       string
	}{
		{name: "Root", redirects: []contain.Redirect{{UID: 0, Port: 3306, LocalPort: 50070}},
			err: "redirect 0:3306=50070 must not name uid 0, which would redirect every process running as root"},
		{name: "Repeated", redirects: []contain.Redirect{{UID: 1000, Port: 3306, LocalPort: 50070}, {UID: 1000, Port: 3306, LocalPort: 50071}},
			err: "redirect 1000:3306=50071 repeats a user's port"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := contain.Apply(t.Context(), contain.Config{UID: 1002, Redirects: test.redirects})
			assert.EqualError(t, err, test.err)
		})
	}
}

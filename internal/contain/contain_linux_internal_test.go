//go:build linux

package contain

import (
	"net"
	"os"
	"runtime"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// candidate is contained, and its and the reference's MySQL connections redirected.
var candidate = Config{CandidateUID: 1002, Redirects: []Redirect{
	{UID: 1002, Port: 3306, LocalPort: 50071},
	{UID: 1000, Port: 3306, LocalPort: 50070},
}}

func TestInstallReplacesRules(t *testing.T) {
	type installed struct {
		redirect, output [][]expr.Any
	}
	got := inNetworkNamespace(t, func() (installed, error) {
		for range 2 {
			if err := install(candidate); err != nil {
				return installed{}, err
			}
		}
		redirect, err := installedRules("redirect")
		if err != nil {
			return installed{}, err
		}
		output, err := installedRules("output")
		return installed{redirect: redirect, output: output}, err
	})
	assert.Equal(t, installed{redirect: redirectRules(candidate.Redirects), output: rules(candidate)}, got)
}

func TestRedirectReachesTheLocalPort(t *testing.T) {
	// The test runs as root, which only a direct install, not a validated config, may redirect.
	accepted := inNetworkNamespace(t, func() (bool, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return false, err
		}
		defer listener.Close()
		local := uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:forcetypeassert,gosec // A TCP listener's port.
		if err := install(Config{CandidateUID: 1002, Redirects: []Redirect{{UID: 0, Port: 3306, LocalPort: local}}}); err != nil {
			return false, err
		}
		conn, err := net.Dial("tcp", "127.0.0.2:3306")
		if err != nil {
			return false, err
		}
		defer conn.Close()
		reached, err := listener.Accept()
		if err != nil {
			return false, err
		}
		return reached.Close() == nil, nil
	})
	assert.True(t, accepted)
}

// inNetworkNamespace runs run as root on a thread in a network namespace of its own,
// with loopback up, and skips the test when it cannot.
func inNetworkNamespace[T any](t *testing.T, run func() (T, error)) T {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("installing packet filter rules needs root")
	}
	type result struct {
		value   T
		skipped string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		// The thread moves to a network namespace of its own, so it never returns to the pool.
		runtime.LockOSThread()
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			done <- result{skipped: "creating a network namespace: " + err.Error()}
			return
		}
		if err := loopbackUp(); err != nil {
			done <- result{err: err}
			return
		}
		value, err := run()
		done <- result{value: value, err: err}
	}()
	outcome := <-done
	if outcome.skipped != "" {
		t.Skip(outcome.skipped)
	}
	assert.NoError(t, outcome.err)
	return outcome.value
}

// loopbackUp brings up the loopback interface, which a new network namespace has down.
func loopbackUp() error {
	socket, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(socket)
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	request.SetUint16(unix.IFF_UP | unix.IFF_LOOPBACK | unix.IFF_RUNNING)
	return unix.IoctlIfreq(socket, unix.SIOCSIFFLAGS, request)
}

// installedRules reads back the rules of chain.
func installedRules(chain string) ([][]expr.Any, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, err
	}
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: tableName}
	installed, err := conn.GetRules(table, &nftables.Chain{Name: chain, Table: table})
	if err != nil {
		return nil, err
	}
	exprs := make([][]expr.Any, 0, len(installed))
	for _, rule := range installed {
		exprs = append(exprs, rule.Exprs)
	}
	return exprs, nil
}

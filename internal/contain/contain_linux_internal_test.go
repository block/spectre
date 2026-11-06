//go:build linux

package contain

import (
	"os"
	"runtime"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

func TestInstallReplacesRules(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("installing packet filter rules needs root")
	}
	type result struct {
		rules   [][]expr.Any
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
		for range 2 {
			if err := install(1002); err != nil {
				done <- result{err: err}
				return
			}
		}
		installed, err := installedRules()
		done <- result{rules: installed, err: err}
	}()
	outcome := <-done
	if outcome.skipped != "" {
		t.Skip(outcome.skipped)
	}
	assert.NoError(t, outcome.err)
	assert.Equal(t, rules(1002), outcome.rules)
}

// installedRules reads back the rules of the output chain.
func installedRules() ([][]expr.Any, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, err
	}
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: tableName}
	installed, err := conn.GetRules(table, &nftables.Chain{Name: "output", Table: table})
	if err != nil {
		return nil, err
	}
	exprs := make([][]expr.Any, 0, len(installed))
	for _, rule := range installed {
		exprs = append(exprs, rule.Exprs)
	}
	return exprs, nil
}

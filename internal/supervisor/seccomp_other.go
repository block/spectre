//go:build !linux

package supervisor

import "github.com/alecthomas/errors"

// SandboxArg is the first argument that re-execs the supervisor as the egress
// sandbox launcher for a child command.
const SandboxArg = "__spectre_sandbox__"

// ExecSandbox reports that the egress sandbox is unavailable off Linux.
func ExecSandbox(_ []string) error {
	return errors.New("egress sandbox requires Linux")
}

// egressSandboxSupported reports whether the egress sandbox can run here.
func egressSandboxSupported() bool { return false }

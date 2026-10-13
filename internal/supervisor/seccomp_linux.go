package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/alecthomas/errors"
	"golang.org/x/sys/unix"
)

// AUDIT_ARCH values for the architectures SPECTRE releases.
const (
	auditArchX8664   = 0xc000003e
	auditArchAArch64 = 0xc00000b7
)

// seccomp(2) mode and flag used to install the filter across all threads.
const (
	seccompSetModeFilter   = 1
	seccompFilterFlagTSync = 1
)

// SandboxArg is the first argument that re-execs the supervisor as the egress
// sandbox launcher for a child command.
const SandboxArg = "__spectre_sandbox__"

// ExecSandbox installs the egress seccomp filter then replaces the process with
// command, so the filter guards it. It returns only when the filter or exec fails.
func ExecSandbox(command []string) error {
	if len(command) == 0 {
		return errors.New("sandbox requires a command to run")
	}
	if err := applyEgressFilter(); err != nil {
		return errors.Wrap(err, "install egress seccomp filter")
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		return errors.Wrapf(err, "find %q", command[0])
	}
	return errors.Wrapf(syscall.Exec(path, command, os.Environ()), "exec %q", command[0])
}

// applyEgressFilter denies IP socket creation for the whole process. The caller
// is expected to exec immediately after, so the filter guards the real program.
func applyEgressFilter() error {
	arch, err := currentArch()
	if err != nil {
		return err
	}
	raw, err := buildEgressFilter(arch)
	if err != nil {
		return err
	}
	filter := make([]unix.SockFilter, len(raw))
	for i, instruction := range raw {
		filter[i] = unix.SockFilter{Code: instruction.Op, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K}
	}
	// Keep the thread that sets no_new_privs and installs the filter stable.
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return errors.Wrap(err, "set no_new_privs")
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, seccompSetModeFilter, seccompFilterFlagTSync, uintptr(unsafe.Pointer(&program))); errno != 0 {
		return errors.Wrap(errno, "load seccomp filter")
	}
	return nil
}

// egressSandboxSupported reports whether the egress sandbox can run here.
func egressSandboxSupported() bool {
	_, err := currentArch()
	return err == nil
}

// currentArch returns the seccomp ABI pin for the running architecture.
func currentArch() (seccompArch, error) {
	switch runtime.GOARCH {
	case "amd64":
		return seccompArch{auditArch: auditArchX8664, socketNR: unix.SYS_SOCKET}, nil
	case "arm64":
		return seccompArch{auditArch: auditArchAArch64, socketNR: unix.SYS_SOCKET}, nil
	default:
		return seccompArch{}, errors.Errorf("egress sandbox unsupported on %s", runtime.GOARCH)
	}
}

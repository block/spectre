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

// seccomp(2) mode used to install the filter.
const seccompSetModeFilter = 1

// SandboxArg is the first argument that re-execs the supervisor as the egress
// sandbox launcher for a child command.
const SandboxArg = "__spectre_sandbox__"

// sandboxHandoffFD is the inherited socket the launcher sends the filter's
// notification listener over, so the supervisor can answer trapped syscalls.
const sandboxHandoffFD = 3

// ExecSandbox installs the egress seccomp filter, hands its listener to the
// supervisor, then replaces the process with command. It returns only on failure.
func ExecSandbox(command []string) error {
	if len(command) == 0 {
		return errors.New("sandbox requires a command to run")
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		return errors.Wrapf(err, "find %q", command[0])
	}
	// The filter guards only the thread that installs it, which then execs.
	runtime.LockOSThread()
	listener, err := applyEgressFilter()
	if err != nil {
		return errors.Wrap(err, "install egress seccomp filter")
	}
	if err := unix.Sendmsg(sandboxHandoffFD, []byte{0}, unix.UnixRights(listener), nil, 0); err != nil {
		return errors.Wrap(err, "hand the seccomp listener to the supervisor")
	}
	// The candidate must never hold the listener, or it could answer its own syscalls.
	if err := errors.Join(unix.Close(listener), unix.Close(sandboxHandoffFD)); err != nil {
		return errors.Wrap(err, "close the seccomp listener")
	}
	//nolint:forbidigo // propagate the inherited environment across the sandbox re-exec
	return errors.Wrapf(syscall.Exec(path, command, os.Environ()), "exec %q", command[0])
}

// applyEgressFilter installs the egress filter on the calling thread and returns
// the listener that receives its trapped syscalls.
func applyEgressFilter() (listener int, err error) {
	arch, err := currentArch()
	if err != nil {
		return -1, err
	}
	raw, err := buildEgressFilter(arch)
	if err != nil {
		return -1, err
	}
	filter := make([]unix.SockFilter, len(raw))
	for i, instruction := range raw {
		filter[i] = unix.SockFilter{Code: instruction.Op, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return -1, errors.Wrap(err, "set no_new_privs")
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	// Killable waits stop signals from interrupting a syscall the supervisor is handling.
	flags := uintptr(unix.SECCOMP_FILTER_FLAG_NEW_LISTENER | unix.SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV)
	fd, _, errno := unix.Syscall(unix.SYS_SECCOMP, seccompSetModeFilter, flags, uintptr(unsafe.Pointer(&program)))
	if errno == unix.EINVAL {
		fd, _, errno = unix.Syscall(unix.SYS_SECCOMP, seccompSetModeFilter, unix.SECCOMP_FILTER_FLAG_NEW_LISTENER, uintptr(unsafe.Pointer(&program)))
	}
	if errno != 0 {
		return -1, errors.Wrap(errno, "load seccomp filter")
	}
	return int(fd), nil
}

// egressSandboxSupported reports whether the egress sandbox can run here.
func egressSandboxSupported() bool {
	_, err := currentArch()
	return err == nil
}

// currentArch returns the seccomp ABI pin for the running architecture.
func currentArch() (seccompArch, error) {
	pin := seccompArch{socketNR: unix.SYS_SOCKET, connectNR: unix.SYS_CONNECT, ioUringSetupNR: unix.SYS_IO_URING_SETUP}
	switch runtime.GOARCH {
	case "amd64":
		pin.auditArch = auditArchX8664
	case "arm64":
		pin.auditArch = auditArchAArch64
	default:
		return seccompArch{}, errors.Errorf("egress sandbox unsupported on %s", runtime.GOARCH)
	}
	return pin, nil
}

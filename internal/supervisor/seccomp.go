package supervisor

import (
	"github.com/alecthomas/errors"
	"golang.org/x/net/bpf"
)

// seccompArch pins a seccomp filter to one syscall ABI so a process cannot evade
// it by issuing syscalls under a different architecture.
type seccompArch struct {
	auditArch uint32
	socketNR  uint32
}

// seccomp_data field offsets and the socket domains and return actions used by
// the egress filter.
const (
	seccompOffsetNR   = 0
	seccompOffsetArch = 4
	seccompOffsetArg0 = 16

	socketDomainInet  = 2  // AF_INET
	socketDomainInet6 = 10 // AF_INET6

	seccompRetAllow       = 0x7fff0000
	seccompRetKillProcess = 0x80000000
	seccompRetErrno       = 0x00050000
	errnoEACCES           = 13
)

// buildEgressFilter assembles the egress program for arch into raw instructions.
func buildEgressFilter(arch seccompArch) ([]bpf.RawInstruction, error) {
	raw, err := bpf.Assemble(egressProgram(arch))
	return raw, errors.Wrap(err, "assemble egress seccomp filter")
}

// egressProgram returns a classic BPF program that fails IPv4 and IPv6 socket
// creation with EACCES, leaving only non-IP families such as unix domain sockets.
func egressProgram(arch seccompArch) []bpf.Instruction {
	return []bpf.Instruction{
		// Kill the process if its syscall ABI is not the one this filter targets.
		bpf.LoadAbsolute{Off: seccompOffsetArch, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch.auditArch, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetKillProcess},
		// Allow every syscall other than socket.
		bpf.LoadAbsolute{Off: seccompOffsetNR, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch.socketNR, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetAllow},
		// Deny socket(AF_INET, ...) and socket(AF_INET6, ...); allow other families.
		bpf.LoadAbsolute{Off: seccompOffsetArg0, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketDomainInet, SkipTrue: 2},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketDomainInet6, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetAllow},
		bpf.RetConstant{Val: seccompRetErrno | (errnoEACCES & 0xffff)},
	}
}

package supervisor

import (
	"github.com/alecthomas/errors"
	"golang.org/x/net/bpf"
)

// seccompArch pins a seccomp filter to one syscall ABI so a process cannot evade
// it by issuing syscalls under a different architecture.
type seccompArch struct {
	auditArch      uint32
	socketNR       uint32
	connectNR      uint32
	ioUringSetupNR uint32
}

// seccomp_data field offsets, the socket arguments the egress filter inspects,
// and its return actions.
const (
	seccompOffsetNR   = 0
	seccompOffsetArch = 4
	seccompOffsetArg0 = 16
	seccompOffsetArg1 = 24
	seccompOffsetArg2 = 32

	// x32 syscalls share the x86-64 audit arch and set this bit in the number.
	syscallX32Bit = 0x40000000

	socketDomainUnix    = 1  // AF_UNIX
	socketDomainInet    = 2  // AF_INET
	socketDomainInet6   = 10 // AF_INET6
	socketDomainNetlink = 16 // AF_NETLINK
	socketTypeMask      = 0xf
	socketTypeStream    = 1 // SOCK_STREAM
	socketProtocolTCP   = 6 // IPPROTO_TCP

	seccompRetAllow       = 0x7fff0000
	seccompRetKillProcess = 0x80000000
	seccompRetUserNotif   = 0x7fc00000
	seccompRetErrno       = 0x00050000
	errnoEACCES           = 13
	errnoENOSYS           = 38
)

// buildEgressFilter assembles the egress program for arch into raw instructions.
func buildEgressFilter(arch seccompArch) ([]bpf.RawInstruction, error) {
	raw, err := bpf.Assemble(egressProgram(arch))
	return raw, errors.Wrap(err, "assemble egress seccomp filter")
}

// egressProgram hands TCP socket creation and every connect to the supervisor,
// and refuses other IP sockets, families beyond unix and netlink, and io_uring.
func egressProgram(arch seccompArch) []bpf.Instruction {
	return []bpf.Instruction{
		// Kill the process if its syscall ABI is not the one this filter targets.
		bpf.LoadAbsolute{Off: seccompOffsetArch, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch.auditArch, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetKillProcess},
		bpf.LoadAbsolute{Off: seccompOffsetNR, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpGreaterOrEqual, Val: syscallX32Bit, SkipFalse: 1},
		bpf.RetConstant{Val: seccompRetKillProcess},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch.connectNR, SkipFalse: 1},
		bpf.RetConstant{Val: seccompRetUserNotif},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch.ioUringSetupNR, SkipFalse: 1},
		bpf.RetConstant{Val: seccompRetErrno | errnoENOSYS},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: arch.socketNR, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetAllow},
		// socket: allow unix and netlink, inspect IP, refuse every other family.
		bpf.LoadAbsolute{Off: seccompOffsetArg0, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketDomainUnix, SkipFalse: 1},
		bpf.RetConstant{Val: seccompRetAllow},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketDomainNetlink, SkipFalse: 1},
		bpf.RetConstant{Val: seccompRetAllow},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketDomainInet, SkipTrue: 2},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketDomainInet6, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetErrno | errnoEACCES},
		// IP sockets must be TCP streams, which the supervisor creates.
		bpf.LoadAbsolute{Off: seccompOffsetArg1, Size: 4},
		bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: socketTypeMask},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketTypeStream, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetErrno | errnoEACCES},
		bpf.LoadAbsolute{Off: seccompOffsetArg2, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipTrue: 2},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: socketProtocolTCP, SkipTrue: 1},
		bpf.RetConstant{Val: seccompRetErrno | errnoEACCES},
		bpf.RetConstant{Val: seccompRetUserNotif},
	}
}

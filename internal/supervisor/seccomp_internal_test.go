package supervisor

import (
	"encoding/binary"
	"testing"

	"github.com/alecthomas/assert/v2"
	"golang.org/x/net/bpf"
)

// testArch drives the filter logic; the syscall numbers are arbitrary for the simulator.
func testArch() seccompArch {
	return seccompArch{auditArch: 0xc000003e, socketNR: 41, connectNR: 42, ioUringSetupNR: 425}
}

// seccompInput builds a big-endian seccomp_data buffer for the BPF VM simulator.
// The kernel instead reads native-endian seccomp_data at the same offsets.
func seccompInput(nr uint32, arch uint32, args ...uint32) []byte {
	data := make([]byte, 64)
	binary.BigEndian.PutUint32(data[seccompOffsetNR:], nr)
	binary.BigEndian.PutUint32(data[seccompOffsetArch:], arch)
	for i, arg := range args {
		binary.BigEndian.PutUint32(data[seccompOffsetArg0+8*i:], arg)
	}
	return data
}

func TestEgressFilter(t *testing.T) {
	arch := testArch()
	vm, err := bpf.NewVM(egressProgram(arch))
	assert.NoError(t, err)

	const sockNonblockCloexec = 0x80800
	for _, test := range []struct {
		name string
		nr   uint32
		arch uint32
		args []uint32
		want int
	}{
		{name: "InetStreamRedirected", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainInet, socketTypeStream, 0}, want: seccompRetUserNotif},
		{name: "Inet6TCPRedirected", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainInet6, socketTypeStream | sockNonblockCloexec, socketProtocolTCP}, want: seccompRetUserNotif},
		{name: "InetDatagramDenied", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainInet, 2, 0}, want: seccompRetErrno | errnoEACCES},
		{name: "InetRawDenied", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainInet6, 3, 0}, want: seccompRetErrno | errnoEACCES},
		{name: "InetStreamSCTPDenied", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainInet, socketTypeStream, 132}, want: seccompRetErrno | errnoEACCES},
		{name: "PacketSocketDenied", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{17, 3, 0}, want: seccompRetErrno | errnoEACCES},
		{name: "UnixSocketAllowed", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainUnix, 2, 0}, want: seccompRetAllow},
		{name: "NetlinkSocketAllowed", nr: arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainNetlink, 3, 0}, want: seccompRetAllow},
		{name: "ConnectTrapped", nr: arch.connectNR, arch: arch.auditArch, want: seccompRetUserNotif},
		{name: "IOUringDenied", nr: arch.ioUringSetupNR, arch: arch.auditArch, want: seccompRetErrno | errnoENOSYS},
		{name: "OtherSyscallAllowed", nr: 1, arch: arch.auditArch, args: []uint32{socketDomainInet}, want: seccompRetAllow},
		{name: "X32SyscallKilled", nr: syscallX32Bit | arch.socketNR, arch: arch.auditArch, args: []uint32{socketDomainInet}, want: seccompRetKillProcess},
		{name: "ForeignArchKilled", nr: arch.socketNR, arch: 0xdeadbeef, args: []uint32{socketDomainInet}, want: seccompRetKillProcess},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := vm.Run(seccompInput(test.nr, test.arch, test.args...))
			assert.NoError(t, err)
			assert.Equal(t, test.want, result)
		})
	}
}

func TestEgressFilterAssembles(t *testing.T) {
	raw, err := buildEgressFilter(testArch())
	assert.NoError(t, err)
	assert.True(t, len(raw) > 0)
}

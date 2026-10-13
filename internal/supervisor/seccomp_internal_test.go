package supervisor

import (
	"encoding/binary"
	"testing"

	"github.com/alecthomas/assert/v2"
	"golang.org/x/net/bpf"
)

// testArch drives the filter logic; socketNR is arbitrary for the simulator.
func testArch() seccompArch { return seccompArch{auditArch: 0xc000003e, socketNR: 41} }

// seccompInput builds a big-endian seccomp_data buffer for the BPF VM simulator.
// The kernel instead reads native-endian seccomp_data at the same offsets.
func seccompInput(nr uint32, arch uint32, arg0 uint32) []byte {
	data := make([]byte, 64)
	binary.BigEndian.PutUint32(data[seccompOffsetNR:], nr)
	binary.BigEndian.PutUint32(data[seccompOffsetArch:], arch)
	binary.BigEndian.PutUint32(data[seccompOffsetArg0:], arg0)
	return data
}

func TestEgressFilter(t *testing.T) {
	arch := testArch()
	vm, err := bpf.NewVM(egressProgram(arch))
	assert.NoError(t, err)

	for _, test := range []struct {
		name string
		nr   uint32
		arch uint32
		arg0 uint32
		want int
	}{
		{name: "inet socket denied", nr: arch.socketNR, arch: arch.auditArch, arg0: socketDomainInet, want: seccompRetErrno | errnoEACCES},
		{name: "inet6 socket denied", nr: arch.socketNR, arch: arch.auditArch, arg0: socketDomainInet6, want: seccompRetErrno | errnoEACCES},
		{name: "unix socket allowed", nr: arch.socketNR, arch: arch.auditArch, arg0: 1, want: seccompRetAllow},
		{name: "other syscall allowed", nr: arch.socketNR + 1, arch: arch.auditArch, arg0: socketDomainInet, want: seccompRetAllow},
		{name: "foreign arch killed", nr: arch.socketNR, arch: 0xdeadbeef, arg0: socketDomainInet, want: seccompRetKillProcess},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := vm.Run(seccompInput(test.nr, test.arch, test.arg0))
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

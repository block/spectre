//go:build linux

package supervisor

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/assert/v2"
	"golang.org/x/sys/unix"
)

// probeArg re-execs the test binary as a sandboxed candidate that reports what
// its network calls reach.
const probeArg = "__spectre_probe__"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case SandboxArg:
			fmt.Fprintln(os.Stderr, ExecSandbox(os.Args[2:]))
			os.Exit(1)
		case probeArg:
			runProbe(os.Args[2])
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// runProbe prints one line per network call the sandboxed candidate attempts.
func runProbe(dir string) {
	conn, err := net.Dial("tcp", "203.0.113.7:80")
	if err == nil {
		_, err = fmt.Fprint(conn, "GET /probe HTTP/1.1\r\nHost: dependency.test\r\nConnection: close\r\n\r\n")
	}
	if err == nil {
		var response *http.Response
		if response, err = http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
			fmt.Println("tcp:", response.Status)
		}
	}
	if err != nil {
		fmt.Println("tcp:", err)
	}
	if _, err := net.Dial("udp", "203.0.113.7:53"); err != nil {
		fmt.Println("udp: refused")
	}
	socket := filepath.Join(dir, "probe.sock")
	listener, err := net.Listen("unix", socket)
	if err == nil {
		var local net.Conn
		if local, err = net.Dial("unix", socket); err == nil {
			fmt.Println("unix: connected")
			_ = local.Close()
		}
		_ = listener.Close()
	}
	if err != nil {
		fmt.Println("unix:", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, 0, 0); errno == unix.ENOSYS {
		fmt.Println("io_uring: refused")
	}
}

func TestEgressRedirect(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	t.Cleanup(func() { _ = proxy.Close() })
	hosts := make(chan string, 1)
	go func() {
		_ = http.Serve(proxy, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hosts <- request.Host
			writer.WriteHeader(http.StatusTeapot)
		}))
	}()

	selfExe, err := os.Executable()
	assert.NoError(t, err)
	var output bytes.Buffer
	cmd := exec.CommandContext(t.Context(), selfExe, SandboxArg, selfExe, probeArg, t.TempDir())
	cmd.Stderr = os.Stderr
	process := newManagedProcess(roleCandidate, cmd, newServiceLogWriter(&output, roleCandidate))
	cmd.Stdout = &output
	target := netip.MustParseAddrPort(proxy.Addr().String())
	assert.NoError(t, newEgressRedirect(t.Context(), target).sandbox(t.Context(), process))
	assert.NoError(t, process.start())
	assert.NoError(t, process.wait())

	assert.Equal(t, []string{
		"tcp: 418 I'm a teapot",
		"udp: refused",
		"unix: connected",
		"io_uring: refused",
	}, strings.Split(strings.TrimSpace(output.String()), "\n"))
	assert.Equal(t, "dependency.test", <-hosts)
}

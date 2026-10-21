package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/alecthomas/errors"
	"golang.org/x/sys/unix"

	"github.com/block/spectre/internal/logger"
)

// redirectDialTimeout bounds connecting a redirected socket to the egress proxy.
const redirectDialTimeout = 5 * time.Second

// seccompNotif mirrors struct seccomp_notif, a syscall the filter trapped.
type seccompNotif struct {
	ID    uint64
	PID   uint32
	Flags uint32
	NR    int32
	Arch  uint32
	IP    uint64
	Args  [6]uint64
}

// seccompNotifResp mirrors struct seccomp_notif_resp, the answer to a trapped syscall.
type seccompNotifResp struct {
	ID    uint64
	Val   int64
	Error int32
	Flags uint32
}

// seccompNotifAddfd mirrors struct seccomp_notif_addfd, which installs a file
// descriptor in the trapped process.
type seccompNotifAddfd struct {
	ID         uint64
	Flags      uint32
	Srcfd      uint32
	Newfd      uint32
	NewfdFlags uint32
}

// egressRedirect answers the candidate's trapped syscalls, so every TCP socket it
// creates arrives already connected to the egress proxy.
type egressRedirect struct {
	target netip.AddrPort
	log    *slog.Logger
	// mu guards sockets, the inodes of the sockets handed to the candidate.
	mu      sync.Mutex
	sockets map[uint64]struct{}
}

// newEgressRedirect returns a redirect to the egress proxy listening on target.
func newEgressRedirect(ctx context.Context, target netip.AddrPort) *egressRedirect {
	return &egressRedirect{target: target, log: logger.FromContext(ctx), sockets: map[uint64]struct{}{}}
}

// sandbox wires process to hand its seccomp listener back once started, then
// answers the listener's syscalls until the sandboxed processes exit or ctx ends.
func (r *egressRedirect) sandbox(ctx context.Context, process *managedProcess) error {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return errors.Wrap(err, "create the sandbox handoff socket")
	}
	child := os.NewFile(uintptr(pair[1]), "sandbox-handoff")
	process.handOff(child, func() error {
		defer unix.Close(pair[0]) //nolint:errcheck // The handoff socket is done once the listener arrives.
		if err := child.Close(); err != nil {
			return errors.Wrap(err, "close the launcher's handoff socket")
		}
		listener, err := receiveListener(pair[0])
		if err != nil {
			return err
		}
		go r.serve(ctx, listener)
		r.log.InfoContext(ctx, "Redirecting candidate TCP sockets to the egress proxy", "target", r.target.String())
		return nil
	})
	return nil
}

// serve answers trapped syscalls on listener until every sandboxed process has
// exited or ctx ends, then closes it.
func (r *egressRedirect) serve(ctx context.Context, listener int) {
	var handlers sync.WaitGroup
	defer func() {
		handlers.Wait()
		_ = unix.Close(listener) //nolint:errcheck // Closing fails pending syscalls, which is the intent.
	}()
	fds := []unix.PollFd{{Fd: int32(listener), Events: unix.POLLIN}}
	for ctx.Err() == nil {
		ready, err := unix.Poll(fds, 200)
		if errors.Is(err, unix.EINTR) || ready == 0 {
			continue
		}
		if err != nil {
			r.log.ErrorContext(ctx, "Egress redirect stopped", "error", err)
			return
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			// The listener hangs up once no sandboxed process remains.
			return
		}
		notif, err := receiveNotif(listener)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			r.log.ErrorContext(ctx, "Egress redirect stopped", "error", err)
			return
		}
		handlers.Go(func() { r.handle(ctx, listener, notif) })
	}
}

// handle answers one trapped syscall.
func (r *egressRedirect) handle(ctx context.Context, listener int, notif seccompNotif) {
	var err error
	switch notif.NR {
	case unix.SYS_SOCKET:
		err = r.redirectSocket(ctx, listener, notif)
	case unix.SYS_CONNECT:
		err = r.answerConnect(listener, notif)
	default:
		err = respond(listener, seccompNotifResp{ID: notif.ID, Error: -int32(unix.ENOSYS)})
	}
	// ENOENT means the process gave up on the syscall, so there is no one to answer.
	if err != nil && !errors.Is(err, unix.ENOENT) {
		r.log.WarnContext(ctx, "Failed to answer a candidate syscall", "pid", notif.PID, "syscall", notif.NR, "error", err)
	}
}

// redirectSocket answers socket() with a socket already connected to the egress
// proxy, or fails it with EACCES when the proxy cannot be reached.
func (r *egressRedirect) redirectSocket(ctx context.Context, listener int, notif seccompNotif) error {
	domain, kind, protocol := int(int32(notif.Args[0])), int(int32(notif.Args[1])), int(int32(notif.Args[2]))
	fd, err := r.dial(domain, protocol, kind&unix.SOCK_NONBLOCK != 0)
	if err != nil {
		r.log.WarnContext(ctx, "Refused a candidate socket: egress proxy unreachable", "pid", notif.PID, "error", err)
		return respond(listener, seccompNotifResp{ID: notif.ID, Error: -int32(unix.EACCES)})
	}
	defer unix.Close(fd) //nolint:errcheck // The candidate holds its own copy once installed.
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return errors.Join(errors.Wrap(err, "stat redirected socket"), respond(listener, seccompNotifResp{ID: notif.ID, Error: -int32(unix.EACCES)}))
	}
	r.remember(stat.Ino)
	addfd := seccompNotifAddfd{ID: notif.ID, Flags: unix.SECCOMP_ADDFD_FLAG_SEND, Srcfd: uint32(fd)}
	if kind&unix.SOCK_CLOEXEC != 0 {
		addfd.NewfdFlags = unix.O_CLOEXEC
	}
	installed, err := ioctl(listener, unix.SECCOMP_IOCTL_NOTIF_ADDFD, unsafe.Pointer(&addfd))
	if err != nil {
		r.forget(stat.Ino)
		return errors.Wrap(err, "install redirected socket")
	}
	r.log.InfoContext(ctx, "Redirected a candidate socket to the egress proxy", "pid", notif.PID, "fd", installed)
	return nil
}

// dial connects a new socket in domain to the egress proxy, leaving it in the
// blocking mode the candidate asked for.
func (r *egressRedirect) dial(domain, protocol int, nonblocking bool) (fd int, err error) {
	fd, err = unix.Socket(domain, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, protocol)
	if err != nil {
		return -1, errors.Wrap(err, "create socket")
	}
	if err := r.connect(fd, domain); err != nil {
		return -1, errors.Join(err, unix.Close(fd))
	}
	if err := unix.SetNonblock(fd, nonblocking); err != nil {
		return -1, errors.Join(errors.Wrap(err, "set socket blocking mode"), unix.Close(fd))
	}
	return fd, nil
}

// connect connects the non-blocking fd to the egress proxy and waits until the
// connection is established.
func (r *egressRedirect) connect(fd, domain int) error {
	var address unix.Sockaddr = &unix.SockaddrInet4{Port: int(r.target.Port()), Addr: r.target.Addr().As4()}
	if domain == unix.AF_INET6 {
		address = &unix.SockaddrInet6{Port: int(r.target.Port()), Addr: r.target.Addr().As16()}
	}
	err := unix.Connect(fd, address)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EINPROGRESS) {
		return errors.Wrap(err, "connect to the egress proxy")
	}
	deadline := time.Now().Add(redirectDialTimeout)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
	for {
		ready, err := unix.Poll(fds, int(time.Until(deadline).Milliseconds()))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return errors.Wrap(err, "wait for the egress proxy")
		}
		if ready == 0 {
			return errors.New("timed out connecting to the egress proxy")
		}
		break
	}
	soErr, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	if err != nil {
		return errors.Wrap(err, "read connect result")
	}
	if soErr != 0 {
		return errors.Wrap(unix.Errno(soErr), "connect to the egress proxy")
	}
	return nil
}

// answerConnect lets connect() succeed on a redirected socket, which is already
// connected. Any other socket proceeds, as it cannot be an IP socket.
func (r *egressRedirect) answerConnect(listener int, notif seccompNotif) error {
	if r.owns(int(notif.PID), int(int32(notif.Args[0]))) {
		return respond(listener, seccompNotifResp{ID: notif.ID})
	}
	return respond(listener, seccompNotifResp{ID: notif.ID, Flags: unix.SECCOMP_USER_NOTIF_FLAG_CONTINUE})
}

// owns reports whether fd in process pid is a socket the redirect handed out.
func (r *egressRedirect) owns(pid, fd int) bool {
	link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fd))
	if err != nil {
		return false
	}
	inode, ok := strings.CutPrefix(link, "socket:[")
	if !ok {
		return false
	}
	number, err := strconv.ParseUint(strings.TrimSuffix(inode, "]"), 10, 64)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, owned := r.sockets[number]
	return owned
}

// remember records a socket handed to the candidate.
func (r *egressRedirect) remember(inode uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sockets[inode] = struct{}{}
}

// forget drops a socket that never reached the candidate.
func (r *egressRedirect) forget(inode uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sockets, inode)
}

// receiveListener reads the seccomp listener the sandbox launcher sends over handoff.
func receiveListener(handoff int) (listener int, err error) {
	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	for {
		n, oobn, _, _, err := unix.Recvmsg(handoff, buf, oob, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return -1, errors.Wrap(err, "receive the seccomp listener")
		}
		if n == 0 {
			return -1, errors.New("sandbox launcher exited before handing over its seccomp listener")
		}
		messages, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil || len(messages) != 1 {
			return -1, errors.Errorf("sandbox launcher sent no seccomp listener: %v", err)
		}
		fds, err := unix.ParseUnixRights(&messages[0])
		if err != nil || len(fds) != 1 {
			return -1, errors.Errorf("sandbox launcher sent no seccomp listener: %v", err)
		}
		return fds[0], nil
	}
}

// receiveNotif reads the next trapped syscall from listener.
func receiveNotif(listener int) (seccompNotif, error) {
	// The kernel rejects a receive buffer that is not zeroed.
	var notif seccompNotif
	_, err := ioctl(listener, unix.SECCOMP_IOCTL_NOTIF_RECV, unsafe.Pointer(&notif))
	return notif, err
}

// respond answers a trapped syscall.
func respond(listener int, response seccompNotifResp) error {
	_, err := ioctl(listener, unix.SECCOMP_IOCTL_NOTIF_SEND, unsafe.Pointer(&response))
	return err
}

// ioctl issues a seccomp listener ioctl, returning its non-negative result.
func ioctl(fd int, request uintptr, arg unsafe.Pointer) (result int, err error) {
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(arg))
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}

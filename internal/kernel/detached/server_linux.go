//go:build linux

package detached

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type CommandHandler interface {
	HandleDetachedKernelCommand(context.Context, CommandRequest) CommandResponse
}

// CommandPostHandler runs only after the response frame has been written. It
// is used for lifecycle commands whose acknowledgement must reach the caller
// before the handler tears down its own control socket.
type CommandPostHandler interface {
	AfterDetachedKernelCommand(context.Context, CommandRequest, CommandResponse)
}

type CommandHandlerFunc func(context.Context, CommandRequest) CommandResponse

func (handler CommandHandlerFunc) HandleDetachedKernelCommand(
	ctx context.Context,
	request CommandRequest,
) CommandResponse {
	return handler(ctx, request)
}

type Server struct {
	SocketPath    string
	Handler       CommandHandler
	MaxConcurrent int
	IdleTimeout   time.Duration
}

func (s Server) Serve(ctx context.Context) error {
	if ctx == nil || s.Handler == nil {
		return errors.New("detached kernel executor server configuration is incomplete")
	}
	if s.MaxConcurrent <= 0 {
		s.MaxConcurrent = 16
	}
	if s.MaxConcurrent > 256 || s.IdleTimeout < 0 {
		return errors.New("detached kernel executor server limits are invalid")
	}
	listener, identity, err := listenPrivateUnixSocket(s.SocketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		unlinkOwnedUnixSocket(s.SocketPath, identity)
	}()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	semaphore := make(chan struct{}, s.MaxConcurrent)
	var active sync.WaitGroup
	defer active.Wait()
	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return acceptErr
		}
		select {
		case semaphore <- struct{}{}:
			active.Add(1)
			go func() {
				defer active.Done()
				defer func() { <-semaphore }()
				s.handleConnection(ctx, connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func (s Server) handleConnection(parent context.Context, connection *net.UnixConn) {
	defer connection.Close()
	if err := validateUnixPeer(connection, uint32(os.Geteuid())); err != nil {
		return
	}
	ctx := parent
	var cancel context.CancelFunc
	if s.IdleTimeout > 0 {
		ctx, cancel = context.WithTimeout(parent, s.IdleTimeout)
		defer cancel()
		if deadline, ok := ctx.Deadline(); ok {
			_ = connection.SetDeadline(deadline)
		}
	}
	frame, err := readBoundedCommandFrame(connection, maxCommandRequestBytes)
	if err != nil {
		return
	}
	request, err := DecodeCommandRequest(frame)
	if err != nil {
		return
	}
	response := func() (response CommandResponse) {
		defer func() {
			if recover() != nil {
				response = commandResponseError(request.RequestID, "internal", "detached kernel command failed")
			}
		}()
		return s.Handler.HandleDetachedKernelCommand(ctx, request)
	}()
	if response.RequestID != request.RequestID {
		response = commandResponseError(request.RequestID, "internal", "detached kernel command response identity is invalid")
	}
	encoded, err := EncodeCommandResponse(response)
	if err != nil {
		encoded, _ = EncodeCommandResponse(commandResponseError(
			request.RequestID, "internal", "detached kernel command response is invalid",
		))
	}
	_ = writeAll(connection, encoded)
	if postHandler, ok := s.Handler.(CommandPostHandler); ok {
		postHandler.AfterDetachedKernelCommand(parent, request, response)
	}
}

type unixSocketIdentity struct {
	device uint64
	inode  uint64
	uid    uint32
}

const maxUnixSocketPathBytes = 107

func listenPrivateUnixSocket(path string) (*net.UnixListener, unixSocketIdentity, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || len([]byte(path)) > maxUnixSocketPathBytes {
		return nil, unixSocketIdentity{}, errors.New("detached kernel socket path is invalid")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, unixSocketIdentity{}, errors.New("prepare detached kernel socket directory")
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return nil, unixSocketIdentity{}, errors.New("secure detached kernel socket directory")
	}
	var parentStat unix.Stat_t
	if err := unix.Lstat(parent, &parentStat); err != nil || parentStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		parentStat.Uid != uint32(os.Geteuid()) || parentStat.Mode&0o077 != 0 {
		return nil, unixSocketIdentity{}, errors.New("detached kernel socket directory is not private")
	}
	if err := unlinkStaleOwnedUnixSocket(path); err != nil {
		return nil, unixSocketIdentity{}, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, unixSocketIdentity{}, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, unixSocketIdentity{}, errors.New("secure detached kernel socket")
	}
	var socket unix.Stat_t
	if err := unix.Lstat(path, &socket); err != nil || socket.Mode&unix.S_IFMT != unix.S_IFSOCK ||
		socket.Uid != uint32(os.Geteuid()) || socket.Mode&0o077 != 0 {
		_ = listener.Close()
		return nil, unixSocketIdentity{}, errors.New("detached kernel socket identity is invalid")
	}
	return listener, unixSocketIdentity{device: uint64(socket.Dev), inode: socket.Ino, uid: socket.Uid}, nil
}

func unlinkStaleOwnedUnixSocket(path string) error {
	var stat unix.Stat_t
	err := unix.Lstat(path, &stat)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("detached kernel socket path is occupied by an untrusted object")
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("detached kernel executor socket is already active")
	}
	if err := unix.Unlink(path); err != nil {
		return errors.New("remove stale detached kernel socket")
	}
	return nil
}

func unlinkOwnedUnixSocket(path string, identity unixSocketIdentity) {
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFSOCK &&
		uint64(stat.Dev) == identity.device && stat.Ino == identity.inode && stat.Uid == identity.uid {
		_ = unix.Unlink(path)
	}
}

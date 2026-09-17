//go:build linux

package detached

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type Client struct {
	SocketPath string
}

func (c Client) Call(ctx context.Context, input CommandRequest) (CommandResponse, error) {
	if ctx == nil {
		return CommandResponse{}, errors.New("detached kernel command context is required")
	}
	if err := validateTrustedUnixSocket(c.SocketPath); err != nil {
		return CommandResponse{}, err
	}
	frame, err := EncodeCommandRequest(input)
	if err != nil {
		return CommandResponse{}, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return CommandResponse{}, fmt.Errorf("connect detached kernel executor: %w", err)
	}
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return CommandResponse{}, errors.New("detached kernel executor did not use a Unix socket")
	}
	if err := validateUnixPeer(unixConnection, uint32(os.Geteuid())); err != nil {
		return CommandResponse{}, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return CommandResponse{}, err
		}
	}
	if err := writeAll(connection, frame); err != nil {
		return CommandResponse{}, fmt.Errorf("write detached kernel command: %w", err)
	}
	responseFrame, err := readBoundedCommandFrame(connection, maxCommandResponseBytes)
	if err != nil {
		return CommandResponse{}, fmt.Errorf("read detached kernel response: %w", err)
	}
	response, err := DecodeCommandResponse(responseFrame)
	if err != nil {
		return CommandResponse{}, err
	}
	if response.RequestID != input.RequestID {
		return CommandResponse{}, errors.New("detached kernel response identity does not match the request")
	}
	if !response.OK {
		return response, &CommandError{Code: response.Code, Message: response.Message}
	}
	return response, nil
}

func validateTrustedUnixSocket(path string) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || len(path) > 107 {
		return errors.New("detached kernel socket path is invalid")
	}
	uid := uint32(os.Geteuid())
	var parent unix.Stat_t
	if err := unix.Lstat(filepath.Dir(path), &parent); err != nil || parent.Mode&unix.S_IFMT != unix.S_IFDIR ||
		parent.Uid != uid || parent.Mode&0o077 != 0 {
		return errors.New("detached kernel socket directory is not private")
	}
	var socket unix.Stat_t
	if err := unix.Lstat(path, &socket); err != nil || socket.Mode&unix.S_IFMT != unix.S_IFSOCK ||
		socket.Uid != uid || socket.Mode&0o077 != 0 {
		return errors.New("detached kernel socket is not a private owned socket")
	}
	return nil
}

func validateUnixPeer(connection *net.UnixConn, expectedUID uint32) error {
	raw, err := connection.SyscallConn()
	if err != nil {
		return errors.New("inspect detached kernel socket peer")
	}
	var credential *unix.Ucred
	var controlErr error
	if err := raw.Control(func(descriptor uintptr) {
		credential, controlErr = unix.GetsockoptUcred(int(descriptor), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return errors.New("inspect detached kernel socket peer")
	}
	if controlErr != nil || credential == nil || credential.Uid != expectedUID || credential.Pid <= 0 {
		return errors.New("detached kernel socket peer identity is not trusted")
	}
	return nil
}

func readBoundedCommandFrame(reader io.Reader, limit int) ([]byte, error) {
	buffered := bufio.NewReaderSize(reader, 32*1024)
	frame := make([]byte, 0, 4096)
	for {
		fragment, prefix, err := buffered.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(frame)+len(fragment)+1 > limit {
			return nil, errors.New("detached kernel command frame exceeds the protocol limit")
		}
		frame = append(frame, fragment...)
		if !prefix {
			frame = append(frame, '\n')
			return frame, nil
		}
	}
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written <= 0 {
			return syscall.EIO
		}
		value = value[written:]
	}
	return nil
}

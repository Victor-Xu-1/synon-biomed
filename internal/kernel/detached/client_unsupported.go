//go:build !linux

package detached

import (
	"context"
	"errors"
)

type Client struct {
	SocketPath string
}

func (Client) Call(context.Context, CommandRequest) (CommandResponse, error) {
	return CommandResponse{}, errors.New("detached kernel execution requires Linux")
}

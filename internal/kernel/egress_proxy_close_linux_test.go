//go:build linux

package kernel

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestKernelEgressCloseReleasesBothTransferSockets(t *testing.T) {
	proxy, err := startKernelEgressProxy(t.TempDir(), "close-transfer", []string{"*"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var readers []chan error
	for range 2 {
		local, remote := net.Pipe()
		defer remote.Close()
		if !proxy.trackConnection(local) {
			t.Fatal("open proxy rejected connection")
		}
		t.Cleanup(func() { _ = local.Close() })
		finished := make(chan error, 1)
		go func() { var data [1]byte; _, err := remote.Read(data[:]); finished <- err }()
		readers = append(readers, finished)
	}
	proxy.Close()
	for _, finished := range readers {
		select {
		case err := <-finished:
			if err == nil {
				t.Error("closed tunnel returned success")
			}
		case <-time.After(time.Second):
			t.Error("stalled tunnel survived proxy close")
		}
	}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	if proxy.trackConnection(local) {
		t.Fatal("closed proxy admitted a late dial")
	}
}

func TestKernelEgressProxyConcurrentCloseWithTransferredChild(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		proxy, err := startKernelEgressProxy(t.TempDir(), "close-control", []string{"example.com"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		child := proxy.takeChild()
		if child == nil {
			t.Fatal("control child descriptor was not transferred")
		}
		// A worker owns this descriptor now. Closing the proxy must unblock its
		// receive loop without waiting for that independent owner to exit.
		var closers sync.WaitGroup
		for range 8 {
			closers.Go(proxy.Close)
		}
		closed := make(chan struct{})
		go func() {
			closers.Wait()
			close(closed)
		}()
		select {
		case <-closed:
			_ = child.Close()
		case <-time.After(5 * time.Second):
			_ = child.Close() // Release the peer before reporting a failed close.
			t.Fatal("concurrent proxy close did not interrupt the control receive")
		}
		select {
		case <-proxy.done:
		default:
			t.Fatal("proxy close returned before the control receive loop exited")
		}
	}
}

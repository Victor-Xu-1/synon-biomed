//go:build linux

package kernel

import (
	"sync"
	"testing"
	"time"
)

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

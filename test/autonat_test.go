//go:build ignore
// +build ignore

// This separate testing package helps to resolve a circular dependency potentially
// being created between libp2p and libp2p-autonat
package autonat_test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	autonat "github.com/libp2p/go-libp2p-autonat"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/network"
)

func TestAutonatRoundtrip(t *testing.T) {
	t.Skip("this test doesn't work")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3 hosts are used: [client] and [service + dialback dialer]
	client, err := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.EnableNATService())
	if err != nil {
		t.Fatal(err)
	}
	service, err := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	dialback, err := libp2p.New(ctx, libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := autonat.New(service, autonat.EnableService(dialback.Network())); err != nil {
		t.Fatal(err)
	}

	client.Peerstore().AddAddrs(service.ID(), service.Addrs(), time.Hour)
	if err := client.Connect(ctx, service.Peerstore().PeerInfo(service.ID())); err != nil {
		t.Fatal(err)
	}

	cSub, err := client.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		t.Fatal(err)
	}
	defer cSub.Close()

	select {
	case stat := <-cSub.Out():
		if stat == network.ReachabilityUnknown {
			t.Fatalf("After status update, client did not know its status")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("sub timed out.")
	}
}

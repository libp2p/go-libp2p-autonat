package autonat

import (
	"context"
	"net"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	host "github.com/libp2p/go-libp2p-host"
)

func init() {
	AutoNATBootDelay = 1 * time.Second
	AutoNATRefreshInterval = 1 * time.Second
	AutoNATRetryInterval = 1 * time.Second
	AutoNATIdentifyDelay = 100 * time.Millisecond
}

func makeAutoNAT(ctx context.Context, t *testing.T) (host.Host, AutoNAT) {
	h, err := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}

	a := NewAutoNAT(ctx, h)

	return h, a
}

// Note: these tests assume the host has only private inet addresses!
func TestAutoNATPrivate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs, _ := makeAutoNATService(ctx, t)
	hc, an := makeAutoNAT(ctx, t)

	status := an.Status()
	if status != NATStatusUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	time.Sleep(2 * time.Second)

	status = an.Status()
	if status != NATStatusPrivate {
		t.Fatalf("unexpected NAT status: %d", status)
	}
}

func TestAutoNATPublic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save := private4
	private4 = []*net.IPNet{}

	hs, _ := makeAutoNATService(ctx, t)
	hc, an := makeAutoNAT(ctx, t)

	status := an.Status()
	if status != NATStatusUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	time.Sleep(2 * time.Second)

	status = an.Status()
	if status != NATStatusPublic {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	private4 = save
}

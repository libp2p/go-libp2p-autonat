package autonat

import (
	"context"
	"net"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	host "github.com/libp2p/go-libp2p-host"
)

func makeAutoNAT(ctx context.Context, t *testing.T) (host.Host, AutoNAT) {
	h, err := libp2p.New(ctx)
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

	save1 := AutoNATBootDelay
	AutoNATBootDelay = 1 * time.Second
	save2 := AutoNATRefreshInterval
	AutoNATRefreshInterval = 1 * time.Second

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

	AutoNATBootDelay = save1
	AutoNATRefreshInterval = save2
}

func TestAutoNATPublic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save1 := AutoNATBootDelay
	AutoNATBootDelay = 1 * time.Second
	save2 := AutoNATRefreshInterval
	AutoNATRefreshInterval = 1 * time.Second
	save3 := private4
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

	AutoNATBootDelay = save1
	AutoNATRefreshInterval = save2
	private4 = save3
}

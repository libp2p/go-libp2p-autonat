package autonat

import (
	"context"
	"net"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	host "github.com/libp2p/go-libp2p-host"
	pstore "github.com/libp2p/go-libp2p-peerstore"
)

func makeAutoNATService(ctx context.Context, t *testing.T) (host.Host, *AutoNATService) {
	h, err := libp2p.New(ctx)
	if err != nil {
		t.Fatal(err)
	}

	as, err := NewAutoNATService(ctx, h)
	if err != nil {
		t.Fatal(err)
	}

	return h, as
}

func makeAutoNATClient(ctx context.Context, t *testing.T) (host.Host, AutoNATClient) {
	h, err := libp2p.New(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cli := NewAutoNATClient(h)
	return h, cli
}

func connect(t *testing.T, a, b host.Host) {
	pinfo := pstore.PeerInfo{ID: a.ID(), Addrs: a.Addrs()}
	err := b.Connect(context.Background(), pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

// Note: these tests assume that the host has only private inet addresses!
func TestAutoNATServiceDialError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save := AutoNATServiceDialTimeout
	AutoNATServiceDialTimeout = 1 * time.Second

	hs, _ := makeAutoNATService(ctx, t)
	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, hs, hc)

	_, err := ac.DialBack(ctx, hs.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialError(err) {
		t.Fatal(err)
	}

	AutoNATServiceDialTimeout = save
}

func TestAutoNATServiceDialSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save := private4
	private4 = []*net.IPNet{}

	hs, _ := makeAutoNATService(ctx, t)
	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, hs, hc)

	_, err := ac.DialBack(ctx, hs.ID())
	if err != nil {
		t.Fatalf("Dial back failed: %s", err.Error())
	}

	private4 = save
}

func TestAutoNATServiceDialRateLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save1 := AutoNATServiceDialTimeout
	AutoNATServiceDialTimeout = 1 * time.Second
	save2 := AutoNATServiceResetInterval
	AutoNATServiceResetInterval = 1 * time.Second
	save3 := private4
	private4 = []*net.IPNet{}

	hs, _ := makeAutoNATService(ctx, t)
	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, hs, hc)

	_, err := ac.DialBack(ctx, hs.ID())
	if err != nil {
		t.Fatal(err)
	}

	_, err = ac.DialBack(ctx, hs.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialRefused(err) {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)

	_, err = ac.DialBack(ctx, hs.ID())
	if err != nil {
		t.Fatal(err)
	}

	AutoNATServiceDialTimeout = save1
	AutoNATServiceResetInterval = save2
	private4 = save3
}

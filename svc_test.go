package autonat

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"

	autonat "github.com/libp2p/go-libp2p-autonat"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

func makeAutoNATService(ctx context.Context, t *testing.T) (host.Host, *AutoNATService) {
	h, err := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}

	as, err := NewAutoNATService(ctx, h)
	if err != nil {
		t.Fatal(err)
	}

	return h, as
}

func makeAutoNATClient(ctx context.Context, t *testing.T) (host.Host, autonat.AutoNATClient) {
	h, err := libp2p.New(ctx, libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}

	cli := autonat.NewAutoNATClient(h, nil)
	return h, cli
}

func connect(t *testing.T, a, b host.Host) {
	pinfo := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()}
	err := b.Connect(context.Background(), pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

// Note: these tests assume that the host has only private network addresses!
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

	if !autonat.IsDialError(err) {
		t.Fatal(err)
	}

	AutoNATServiceDialTimeout = save
}

func TestAutoNATServiceDialSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save := manet.Private4
	manet.Private4 = []*net.IPNet{}

	hs, _ := makeAutoNATService(ctx, t)
	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, hs, hc)

	_, err := ac.DialBack(ctx, hs.ID())
	if err != nil {
		t.Fatalf("Dial back failed: %s", err.Error())
	}

	manet.Private4 = save
}

func TestAutoNATServiceDialRateLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save1 := AutoNATServiceDialTimeout
	AutoNATServiceDialTimeout = 1 * time.Second
	save2 := AutoNATServiceResetInterval
	AutoNATServiceResetInterval = 1 * time.Second
	save3 := AutoNATServiceThrottle
	AutoNATServiceThrottle = 1
	save4 := manet.Private4
	manet.Private4 = []*net.IPNet{}
	save5 := AutoNATServiceResetJitter
	AutoNATServiceResetJitter = 0 * time.Second

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

	if !autonat.IsDialRefused(err) {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)

	_, err = ac.DialBack(ctx, hs.ID())
	if err != nil {
		t.Fatal(err)
	}

	AutoNATServiceDialTimeout = save1
	AutoNATServiceResetInterval = save2
	AutoNATServiceThrottle = save3
	manet.Private4 = save4
	AutoNATServiceResetJitter = save5
}

func TestAutoNATServiceRateLimitJitter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	save1 := AutoNATServiceResetInterval
	AutoNATServiceResetInterval = 100 * time.Millisecond
	save2 := AutoNATServiceResetJitter
	AutoNATServiceResetJitter = 100 * time.Millisecond

	_, svc := makeAutoNATService(ctx, t)
	svc.globalReqs = 1
	time.Sleep(200 * time.Millisecond)
	if svc.globalReqs != 0 {
		t.Fatal("reset of rate limitter occured slower than expected")
	}

	AutoNATServiceResetInterval = save1
	AutoNATServiceResetJitter = save2
}

func TestAddrToIP(t *testing.T) {
	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/0")
	if ip, err := addrToIP(addr); err != nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatal("addrToIP of ipv4 localhost incorrect!")
	}

	addr, _ = ma.NewMultiaddr("/ip4/192.168.0.1/tcp/6")
	if ip, err := addrToIP(addr); err != nil || !ip.Equal(net.IPv4(192, 168, 0, 1)) {
		t.Fatal("addrToIP of ipv4 incorrect!")
	}

	addr, _ = ma.NewMultiaddr("/ip6zone/eth0/ip6/fe80::1")
	if ip, err := addrToIP(addr); err != nil || !ip.Equal(net.ParseIP("fe80::1")) {
		t.Fatal("addrToIP of ip6zone incorrect!")
	}

	addr, _ = ma.NewMultiaddr("/unix/a/b/c/d")
	if _, err := addrToIP(addr); err == nil {
		t.Fatal("invalid addrToIP populates")
	}
}

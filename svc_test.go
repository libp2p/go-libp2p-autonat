package autonat

import (
	"context"
	"testing"
	"time"

	bhost "github.com/libp2p/go-libp2p-blankhost"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"

	ma "github.com/multiformats/go-multiaddr"
)

func makeAutoNATConfig(ctx context.Context, t *testing.T) *config {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	dh := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	c := config{host: h, dialer: dh.Network()}
	_ = defaults(&c)
	c.forceReachability = true
	c.dialPolicy.allowSelfDials = true
	return &c
}

func makeAutoNATService(ctx context.Context, t *testing.T, c *config) *autoNATService {
	as, err := newAutoNATService(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	as.Enable()

	return as
}

func makeAutoNATClient(ctx context.Context, t *testing.T) (host.Host, Client) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	cli := NewAutoNATClient(h, nil)
	return h, cli
}

// Note: these tests assume that the host has only private network addresses!
func TestAutoNATServiceDialError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(ctx, t)
	c.dialTimeout = 1 * time.Second
	c.dialPolicy.allowSelfDials = false
	_ = makeAutoNATService(ctx, t, c)
	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, c.host, hc)

	_, err := ac.DialBack(ctx, c.host.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialError(err) {
		t.Fatal(err)
	}
}

func TestAutoNATServiceDialSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(ctx, t)
	_ = makeAutoNATService(ctx, t, c)

	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, c.host, hc)

	_, err := ac.DialBack(ctx, c.host.ID())
	if err != nil {
		t.Fatalf("Dial back failed: %s", err.Error())
	}
}

func TestAutoNATServiceDialRateLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(ctx, t)
	c.dialTimeout = 1 * time.Second
	c.throttleResetPeriod = time.Second
	c.throttleResetJitter = 0
	c.throttlePeerMax = 1
	_ = makeAutoNATService(ctx, t, c)

	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, c.host, hc)

	_, err := ac.DialBack(ctx, c.host.ID())
	if err != nil {
		t.Fatal(err)
	}

	_, err = ac.DialBack(ctx, c.host.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialRefused(err) {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)

	_, err = ac.DialBack(ctx, c.host.ID())
	if err != nil {
		t.Fatal(err)
	}
}

func TestAutoNATServiceGlobalLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := makeAutoNATConfig(ctx, t)
	c.dialTimeout = time.Second
	c.throttleResetPeriod = 10 * time.Second
	c.throttleResetJitter = 0
	c.throttlePeerMax = 1
	c.throttleGlobalMax = 5
	_ = makeAutoNATService(ctx, t, c)

	hs := c.host

	for i := 0; i < 5; i++ {
		hc, ac := makeAutoNATClient(ctx, t)
		connect(t, hs, hc)

		_, err := ac.DialBack(ctx, hs.ID())
		if err != nil {
			t.Fatal(err)
		}
	}

	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, hs, hc)
	_, err := ac.DialBack(ctx, hs.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialRefused(err) {
		t.Fatal(err)
	}
}

func TestAutoNATServiceRateLimitJitter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	c := makeAutoNATConfig(ctx, t)
	c.throttleResetPeriod = 100 * time.Millisecond
	c.throttleResetJitter = 100 * time.Millisecond
	c.throttleGlobalMax = 1
	svc := makeAutoNATService(ctx, t, c)
	svc.mx.Lock()
	svc.globalReqs = 1
	svc.mx.Unlock()
	time.Sleep(200 * time.Millisecond)

	svc.mx.Lock()
	defer svc.mx.Unlock()
	if svc.globalReqs != 0 {
		t.Fatal("reset of rate limitter occured slower than expected")
	}

	cancel()
}

func TestAutoNATServiceStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	dh := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	an, err := New(ctx, h, EnableService(dh.Network()))
	an.(*AmbientAutoNAT).config.dialPolicy.allowSelfDials = true
	if err != nil {
		t.Fatal(err)
	}

	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, h, hc)

	_, err = ac.DialBack(ctx, h.ID())
	if err != nil {
		t.Fatal("autonat service be active in unknown mode.")
	}

	sub, _ := h.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))

	anc := an.(*AmbientAutoNAT)
	anc.recordObservation(autoNATResult{Reachability: network.ReachabilityPublic, address: ma.StringCast("/ip4/127.0.0.1/tcp/1234")})

	<-sub.Out()

	_, err = ac.DialBack(ctx, h.ID())
	if err != nil {
		t.Fatalf("autonat should be active, was %v", err)
	}
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("autonat should report public, but didn't")
	}
}

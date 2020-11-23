package autonat

import (
	"context"
	"sort"
	"testing"
	"time"

	bhost "github.com/libp2p/go-libp2p-blankhost"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/transport"
	quic "github.com/libp2p/go-libp2p-quic-transport"
	swarm "github.com/libp2p/go-libp2p-swarm"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	"github.com/libp2p/go-tcp-transport"
	"github.com/stretchr/testify/require"

	ma "github.com/multiformats/go-multiaddr"
)

func makeAutoNATConfig(ctx context.Context, t *testing.T) *config {
	sw := swarmt.GenSwarm(t, ctx)
	h := bhost.NewBlankHost(sw)
	ts := mkTransports(t, sw)
	c := config{host: h, transports: ts}
	_ = defaults(&c)
	c.forceReachability = true
	c.dialPolicy.allowSelfDials = true
	return &c
}

func mkTransports(t *testing.T, sw *swarm.Swarm) []transport.Transport {
	u := swarmt.GenUpgrader(sw)

	tcpTransport := tcp.NewTCPTransport(u)

	quicTransport, err := quic.NewTransport(sw.Peerstore().PrivKey(sw.LocalPeer()), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	return []transport.Transport{tcpTransport, quicTransport}
}

func makeAutoNATService(ctx context.Context, t *testing.T, c *config) *autoNATService {
	as, err := newAutoNATService(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	as.Enable()

	return as
}

func makeAutoNATClient(ctx context.Context, t *testing.T, opts ...swarmt.Option) (host.Host, Client) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx, opts...))
	cli := NewAutoNATClient(h, nil)
	return h, cli
}

func makeAutoNATClientWithAddrFunc(ctx context.Context, t *testing.T, addrFunc AddrFunc, opts ...swarmt.Option) (host.Host, Client) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx, opts...))
	cli := NewAutoNATClient(h, addrFunc)
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

	_, _, err := ac.DialBack(ctx, c.host.ID())
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

	_, _, err := ac.DialBack(ctx, c.host.ID())
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

	_, _, err := ac.DialBack(ctx, c.host.ID())
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = ac.DialBack(ctx, c.host.ID())
	if err == nil {
		t.Fatal("Dial back succeeded unexpectedly!")
	}

	if !IsDialRefused(err) {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)

	_, _, err = ac.DialBack(ctx, c.host.ID())
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

		_, _, err := ac.DialBack(ctx, hs.ID())
		if err != nil {
			t.Fatal(err)
		}
	}

	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, hs, hc)
	_, _, err := ac.DialBack(ctx, hs.ID())
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

	sw := swarmt.GenSwarm(t, ctx)
	h := bhost.NewBlankHost(sw)
	an, err := New(ctx, h, EnableService(mkTransports(t, sw)...))
	an.(*AmbientAutoNAT).config.dialPolicy.allowSelfDials = true
	if err != nil {
		t.Fatal(err)
	}

	hc, ac := makeAutoNATClient(ctx, t)
	connect(t, h, hc)

	_, _, err = ac.DialBack(ctx, h.ID())
	if err != nil {
		t.Fatal("autonat service be active in unknown mode.")
	}

	sub, _ := h.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))

	anc := an.(*AmbientAutoNAT)
	anc.recordObservation(autoNATResult{Reachability: network.ReachabilityPublic, address: ma.StringCast("/ip4/127.0.0.1/tcp/1234")})

	<-sub.Out()

	_, _, err = ac.DialBack(ctx, h.ID())
	if err != nil {
		t.Fatalf("autonat should be active, was %v", err)
	}
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("autonat should report public, but didn't")
	}
}

func TestAddressDiversityDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tcs := map[string]struct {
		listenAddrs          []string // addresses the client host will listen on
		addrsParams          []string // addresses we will ask the server to dial
		expectedFailedAddrs  []string // successes we expect
		expectedSuccessAddrs []string // failures we expect
	}{
		"one IPV4/tcp -> one success": {
			listenAddrs:          []string{"/ip4/127.0.0.1/tcp/2001"},
			addrsParams:          []string{"/ip4/127.0.0.1/tcp/2001"},
			expectedSuccessAddrs: []string{"/ip4/127.0.0.1/tcp/2001"},
		},
		"one tcp, one quic -> both success": {
			listenAddrs:          []string{"/ip4/127.0.0.1/tcp/5001", "/ip4/0.0.0.0/udp/5001/quic"},
			addrsParams:          []string{"/ip4/127.0.0.1/tcp/5001", "/ip4/127.0.0.1/udp/5001/quic"},
			expectedSuccessAddrs: []string{"/ip4/127.0.0.1/tcp/5001", "/ip4/127.0.0.1/udp/5001/quic"},
		},
		"three tcp, two quic -> two success from each": {
			listenAddrs: []string{"/ip4/127.0.0.1/tcp/6001", "/ip4/127.0.0.1/tcp/6002", "/ip4/127.0.0.1/tcp/6003",
				"/ip4/0.0.0.0/udp/6001/quic", "/ip4/0.0.0.0/udp/6002/quic"},

			addrsParams: []string{"/ip4/127.0.0.1/tcp/6001", "/ip4/127.0.0.1/tcp/6002", "/ip4/127.0.0.1/tcp/6003",
				"/ip4/127.0.0.1/udp/6001/quic", "/ip4/127.0.0.1/udp/6002/quic"},

			expectedSuccessAddrs: []string{"/ip4/127.0.0.1/tcp/6001", "/ip4/127.0.0.1/udp/6001/quic", "/ip4/127.0.0.1/tcp/6002",
				"/ip4/127.0.0.1/udp/6002/quic"},
		},
		"four tcp, four quic, three tcp and two quic fail -> so picks one tcp and two quic": {
			listenAddrs: []string{"/ip4/127.0.0.1/tcp/7001", "/ip4/127.0.0.1/udp/7001/quic", "/ip4/127.0.0.1/udp/7002/quic"},

			addrsParams: []string{"/ip4/127.0.0.1/tcp/7001", "/ip4/127.0.0.1/tcp/7002", "/ip4/127.0.0.1/tcp/7003", "/ip4/127.0.0.1/tcp/7004",
				"/ip4/127.0.0.1/udp/7001/quic", "/ip4/127.0.0.1/udp/7002/quic", "/ip4/127.0.0.1/udp/7003/quic", "/ip4/127.0.0.1/udp/7004/quic"},

			expectedSuccessAddrs: []string{"/ip4/127.0.0.1/tcp/7001", "/ip4/127.0.0.1/udp/7001/quic", "/ip4/127.0.0.1/udp/7002/quic"},

			expectedFailedAddrs: []string{"/ip4/127.0.0.1/tcp/7002", "/ip4/127.0.0.1/tcp/7003", "/ip4/127.0.0.1/tcp/7004", "/ip4/127.0.0.1/udp/7003/quic",
				"/ip4/127.0.0.1/udp/7004/quic"},
		},

		"four tcp, three quic, two tcp and two quic fail -> so picks two tcp and one quic": {
			listenAddrs: []string{"/ip4/127.0.0.1/tcp/8001", "/ip4/127.0.0.1/tcp/8002", "/ip4/127.0.0.1/udp/8001/quic"},

			addrsParams: []string{"/ip4/127.0.0.1/tcp/8001", "/ip4/127.0.0.1/tcp/8002", "/ip4/127.0.0.1/tcp/8003", "/ip4/127.0.0.1/tcp/8004",
				"/ip4/127.0.0.1/udp/8001/quic", "/ip4/127.0.0.1/udp/8002/quic", "/ip4/127.0.0.1/udp/8003/quic"},

			expectedSuccessAddrs: []string{"/ip4/127.0.0.1/tcp/8001", "/ip4/127.0.0.1/tcp/8002", "/ip4/127.0.0.1/udp/8001/quic"},

			expectedFailedAddrs: []string{"/ip4/127.0.0.1/tcp/8003", "/ip4/127.0.0.1/tcp/8004", "/ip4/127.0.0.1/udp/8002/quic", "/ip4/127.0.0.1/udp/8003/quic"},
		},
	}

	c := makeAutoNATConfig(ctx, t)
	c.throttleGlobalMax = 1000
	c.throttlePeerMax = 1000
	_ = makeAutoNATService(ctx, t, c)

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			time.Sleep(100 * time.Millisecond)
			require := require.New(t)

			addrF := func() []ma.Multiaddr {
				return multiAddrsFromString(tc.addrsParams)
			}
			hc, ac := makeAutoNATClientWithAddrFunc(ctx, t, addrF, swarmt.OptDialOnly)
			defer hc.Close()
			require.NoError(hc.Network().Listen(multiAddrsFromString(tc.listenAddrs)...))

			time.Sleep(100 * time.Millisecond)

			connect(t, c.host, hc)

			conns := hc.Network().ConnsToPeer(c.host.ID())
			localConnAddr := conns[0].LocalMultiaddr()

			// remove the address with which the client dials the server from the list of failed addresses
			success, failed, err := ac.DialBack(ctx, c.host.ID())
			require.NoError(err)
			for i, a := range failed {
				if a.Equal(localConnAddr) {
					failed[i] = failed[len(failed)-1]
					failed = failed[:len(failed)-1]
					break
				}
			}

			multiAddrsEqual(require, multiAddrsFromString(tc.expectedSuccessAddrs), success)
			multiAddrsEqual(require, multiAddrsFromString(tc.expectedFailedAddrs), failed)
		})
	}
}

func multiAddrsEqual(require *require.Assertions, expected []ma.Multiaddr, actual []ma.Multiaddr) {
	require.Equalf(len(expected), len(actual), "expected: %v, actual: %v", expected, actual)

	mastr1 := multiAddrsToString(expected)
	mastr2 := multiAddrsToString(actual)
	sort.Strings(mastr1)
	sort.Strings(mastr2)
	require.Equal(mastr1, mastr2)
}

func multiAddrsToString(mas []ma.Multiaddr) []string {
	if len(mas) == 0 {
		return nil
	}
	var addrs []string
	for _, ma := range mas {
		addrs = append(addrs, ma.String())
	}
	return addrs
}

func multiAddrsFromString(s []string) []ma.Multiaddr {
	if len(s) == 0 {
		return nil
	}
	var mas []ma.Multiaddr
	for _, a := range s {
		mas = append(mas, ma.StringCast(a))
	}

	return mas
}

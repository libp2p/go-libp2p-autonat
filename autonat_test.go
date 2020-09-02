package autonat

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	pb "github.com/libp2p/go-libp2p-autonat/pb"

	bhost "github.com/libp2p/go-libp2p-blankhost"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	"github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"
)

// these are mock service implementations for testing
func makeAutoNATServicePrivate(ctx context.Context, t *testing.T) host.Host {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	h.SetStreamHandler(AutoNATProto, sayAutoNATPrivate)
	return h
}

func makeAutoNATServicePublic(ctx context.Context, t *testing.T) host.Host {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	h.SetStreamHandler(AutoNATProto, sayAutoNATPublic)
	return h
}

func sayAutoNATPrivate(s network.Stream) {
	defer s.Close()
	w := protoio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: newDialResponseError(pb.Message_E_DIAL_ERROR, "no dialable addresses"),
	}
	w.WriteMsg(&res)
}

func sayAutoNATPublic(s network.Stream) {
	defer s.Close()
	w := protoio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: newDialResponseOK(s.Conn().RemoteMultiaddr()),
	}
	w.WriteMsg(&res)
}

func makeAutoNAT(ctx context.Context, t *testing.T, ash host.Host) (host.Host, AutoNAT) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	h.Peerstore().AddAddrs(ash.ID(), ash.Addrs(), time.Minute)
	h.Peerstore().AddProtocols(ash.ID(), AutoNATProto)
	a, _ := New(ctx, h, WithSchedule(100*time.Millisecond, time.Second), WithoutStartupDelay())
	a.(*AmbientAutoNAT).config.dialPolicy.allowSelfDials = true
	a.(*AmbientAutoNAT).config.throttlePeerPeriod = 100 * time.Millisecond
	return h, a
}

func identifyAsServer(server, recip host.Host) {
	recip.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Minute)
	recip.Peerstore().AddProtocols(server.ID(), AutoNATProto)

}

func connect(t *testing.T, a, b host.Host) {
	pinfo := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()}
	err := b.Connect(context.Background(), pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

func expectEvent(t *testing.T, s event.Subscription, expected network.Reachability) {
	select {
	case e := <-s.Out():
		ev, ok := e.(event.EvtLocalReachabilityChanged)
		if !ok || ev.Reachability != expected {
			t.Fatal("got wrong event type from the bus")
		}

	case <-time.After(100 * time.Millisecond):
		t.Fatal("failed to get the reachability event from the bus")
	}
}

// tests
func TestAutoNATPrivate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs := makeAutoNATServicePrivate(ctx, t)
	hc, an := makeAutoNAT(ctx, t, hs)

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalReachabilityChanged, err=%s", err)
	}

	status := an.Status()
	if status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	time.Sleep(2 * time.Second)

	status = an.Status()
	if status != network.ReachabilityPrivate {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	expectEvent(t, s, network.ReachabilityPrivate)
}

func TestAutoNATPublic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs := makeAutoNATServicePublic(ctx, t)
	hc, an := makeAutoNAT(ctx, t, hs)

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalReachabilityChanged, err=%s", err)
	}

	status := an.Status()
	if status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	time.Sleep(1500 * time.Millisecond)

	status = an.Status()
	if status != network.ReachabilityPublic {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	expectEvent(t, s, network.ReachabilityPublic)
}

func TestAutoNATPublictoPrivate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs := makeAutoNATServicePublic(ctx, t)
	hc, an := makeAutoNAT(ctx, t, hs)

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPublic, err=%s", err)
	}

	status := an.Status()
	if status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	time.Sleep(1500 * time.Millisecond)

	status = an.Status()
	if status != network.ReachabilityPublic {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	expectEvent(t, s, network.ReachabilityPublic)

	hs.SetStreamHandler(AutoNATProto, sayAutoNATPrivate)
	hps := makeAutoNATServicePrivate(ctx, t)
	connect(t, hps, hc)
	identifyAsServer(hps, hc)

	time.Sleep(2 * time.Second)

	expectEvent(t, s, network.ReachabilityPrivate)

	status = an.Status()
	if status != network.ReachabilityPrivate {
		t.Fatalf("unexpected NAT status: %d", status)
	}
}

func TestAutoNATIncomingEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs := makeAutoNATServicePrivate(ctx, t)
	hc, ani := makeAutoNAT(ctx, t, hs)
	an := ani.(*AmbientAutoNAT)

	status := an.Status()
	if status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)

	em, _ := hc.EventBus().Emitter(&event.EvtPeerIdentificationCompleted{})
	em.Emit(event.EvtPeerIdentificationCompleted{Peer: hs.ID()})

	time.Sleep(10 * time.Millisecond)
	if an.Status() == network.ReachabilityUnknown {
		t.Fatalf("Expected probe due to identification of autonat service")
	}
}

func TestAutoNATObservationRecording(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs := makeAutoNATServicePublic(ctx, t)
	hc, ani := makeAutoNAT(ctx, t, hs)
	an := ani.(*AmbientAutoNAT)

	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPublic, err=%s", err)
	}

	// pubic observation without address should be ignored.
	an.recordObservation(autoNATResult{network.ReachabilityPublic, nil})
	if an.Status() != network.ReachabilityUnknown {
		t.Fatalf("unexpected transition")
	}

	select {
	case _ = <-s.Out():
		t.Fatal("not expecting a public reachability event")
	default:
		//expected
	}

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/udp/1234")
	an.recordObservation(autoNATResult{network.ReachabilityPublic, addr})
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("failed to transition to public.")
	}

	expectEvent(t, s, network.ReachabilityPublic)

	// a single recording should have confidence still at 0, and transition to private quickly.
	an.recordObservation(autoNATResult{network.ReachabilityPrivate, nil})
	if an.Status() != network.ReachabilityPrivate {
		t.Fatalf("failed to transition to private.")
	}

	expectEvent(t, s, network.ReachabilityPrivate)

	// stronger public confidence should be harder to undo.
	an.recordObservation(autoNATResult{network.ReachabilityPublic, addr})
	an.recordObservation(autoNATResult{network.ReachabilityPublic, addr})
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("failed to transition to public.")
	}

	expectEvent(t, s, network.ReachabilityPublic)

	an.recordObservation(autoNATResult{network.ReachabilityPrivate, nil})
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("too-extreme private transition.")
	}

}

func TestStaticNat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	s, _ := h.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})

	nat, err := New(ctx, h, WithReachability(network.ReachabilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if nat.Status() != network.ReachabilityPrivate {
		t.Fatalf("should be private")
	}
	expectEvent(t, s, network.ReachabilityPrivate)
}

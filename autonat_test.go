package autonat

import (
	"context"
	"testing"
	"time"

	pb "github.com/libp2p/go-libp2p-autonat/pb"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	ggio "github.com/gogo/protobuf/io"
	bhost "github.com/libp2p/go-libp2p-blankhost"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	ma "github.com/multiformats/go-multiaddr"
)

func init() {
	AutoNATBootDelay = 100 * time.Millisecond
	AutoNATRefreshInterval = 1 * time.Second
	AutoNATRetryInterval = 1 * time.Second
	AutoNATIdentifyDelay = 100 * time.Millisecond
}

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
	w := ggio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: newDialResponseError(pb.Message_E_DIAL_ERROR, "no dialable addresses"),
	}
	w.WriteMsg(&res)
}

func sayAutoNATPublic(s network.Stream) {
	defer s.Close()
	w := ggio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: newDialResponseOK(s.Conn().RemoteMultiaddr()),
	}
	w.WriteMsg(&res)
}

func newDialResponseOK(addr ma.Multiaddr) *pb.Message_DialResponse {
	dr := new(pb.Message_DialResponse)
	dr.Status = pb.Message_OK.Enum()
	dr.Addr = addr.Bytes()
	return dr
}

func newDialResponseError(status pb.Message_ResponseStatus, text string) *pb.Message_DialResponse {
	dr := new(pb.Message_DialResponse)
	dr.Status = status.Enum()
	dr.StatusText = &text
	return dr
}

func makeAutoNAT(ctx context.Context, t *testing.T, ash host.Host) (host.Host, AutoNAT) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	h.Peerstore().AddAddrs(ash.ID(), ash.Addrs(), time.Minute)
	h.Peerstore().AddProtocols(ash.ID(), AutoNATProto)
	a := NewAutoNAT(ctx, h, nil)
	return h, a
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
	time.Sleep(1 * time.Second)

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
	time.Sleep(200 * time.Millisecond)

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
	time.Sleep(200 * time.Millisecond)

	status = an.Status()
	if status != network.ReachabilityPublic {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	expectEvent(t, s, network.ReachabilityPublic)

	hs.SetStreamHandler(AutoNATProto, sayAutoNATPrivate)
	time.Sleep(2 * time.Second)

	status = an.Status()
	if status != network.ReachabilityPrivate {
		t.Fatalf("unexpected NAT status: %d", status)
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

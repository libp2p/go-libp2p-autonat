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
	AutoNATBootDelay = 1 * time.Second
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
	a := NewAutoNAT(ctx, h, nil)
	a.(*AmbientAutoNAT).mx.Lock()
	a.(*AmbientAutoNAT).peers[ash.ID()] = ash.Addrs()
	a.(*AmbientAutoNAT).mx.Unlock()
	return h, a
}

func connect(t *testing.T, a, b host.Host) {
	pinfo := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()}
	err := b.Connect(context.Background(), pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

// tests
func TestAutoNATPrivate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs := makeAutoNATServicePrivate(ctx, t)
	hc, an := makeAutoNAT(ctx, t, hs)

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalRoutabilityPrivate{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPrivate, err=%s", err)
	}

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

	select {
	case e := <-s.Out():
		_, ok := e.(event.EvtLocalRoutabilityPrivate)
		if !ok {
			t.Fatal("got wrong event type from the bus")
		}

	case <-time.After(1 * time.Second):
		t.Fatal("failed to get the EvtLocalRoutabilityPrivate event from the bus")
	}
}

func TestAutoNATPublic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hs := makeAutoNATServicePublic(ctx, t)
	hc, an := makeAutoNAT(ctx, t, hs)

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalRoutabilityPublic{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPublic, err=%s", err)
	}

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

	select {
	case e := <-s.Out():
		_, ok := e.(event.EvtLocalRoutabilityPublic)
		if !ok {
			t.Fatal("got wrong event type from the bus")
		}

	case <-time.After(1 * time.Second):
		t.Fatal("failed to get the EvtLocalRoutabilityPublic event from the bus")
	}
}

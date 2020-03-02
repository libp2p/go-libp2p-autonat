package autonat

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/crypto"
	cryptopb "github.com/libp2p/go-libp2p-core/crypto/pb"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	pb "github.com/libp2p/go-libp2p-autonat/pb"
	bhost "github.com/libp2p/go-libp2p-blankhost"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	tnet "github.com/libp2p/go-libp2p-testing/net"
	ma "github.com/multiformats/go-multiaddr"

	ggio "github.com/gogo/protobuf/io"
	"github.com/multiformats/go-varint"
	"github.com/stretchr/testify/require"
)

func init() {
	AutoNATBootDelay = 1 * time.Second
	AutoNATRefreshInterval = 1 * time.Second
	AutoNATRetryInterval = 1 * time.Second
	AutoNATIdentifyDelay = 100 * time.Millisecond
}

type mockAutoNatService struct {
	t   *testing.T
	ctx context.Context

	h host.Host

	dialer   host.Host
	dialerPk *cryptopb.PublicKey
	dialerSk crypto.PrivKey
}

func mkMockAutoNatService(ctx context.Context, t *testing.T) *mockAutoNatService {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))

	dialer := bhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	sk := dialer.Peerstore().PrivKey(dialer.ID())
	pk := dialer.Peerstore().PubKey(dialer.ID())

	cpk, err := crypto.PublicKeyToProto(pk)
	require.NoError(t, err, "failed to get proto public key")

	return &mockAutoNatService{t, ctx, h, dialer, cpk, sk}
}

func (v *mockAutoNatService) newDialerCertificate(nonce uint64) *pb.Message_DialerIdentityCertificate {
	msg := &pb.Message_DialerIdentityCertificate{}
	msg.PublicKey = v.dialerPk
	s, err := v.dialerSk.Sign(varint.ToUvarint(nonce))
	require.NoError(v.t, err, "failed to sign nonce")
	msg.Signature = s
	return msg
}

func (v *mockAutoNatService) readNonce(s network.Stream) uint64 {
	var req pb.Message
	r := ggio.NewDelimitedReader(s, network.MessageSizeMax)
	err := r.ReadMsg(&req)
	require.NoError(v.t, err, "failed to read dial req")
	return *req.Dial.Nonce
}

func (v *mockAutoNatService) sayPublicNoDial(s network.Stream) {
	defer s.Close()
	// read nonce
	n := v.readNonce(s)

	// send OK response without dialing
	w := ggio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: v.newDialResponseOK(s.Conn().RemoteMultiaddr(), n),
	}
	w.WriteMsg(&res)
}

func (v *mockAutoNatService) sayPublicInvalidPublicKey(s network.Stream) {
	defer s.Close()
	// read nonce
	n := v.readNonce(s)

	// dial
	require.NoError(v.t, v.dialer.Connect(v.ctx, peer.AddrInfo{s.Conn().RemotePeer(), []ma.Multiaddr{s.Conn().RemoteMultiaddr()}}),
		"AutoNat dialer failed to dial back to the client host")

	// use a different public key
	p := tnet.RandPeerNetParamsOrFatal(v.t)
	cpk, err := crypto.PublicKeyToProto(p.PubKey)
	require.NoError(v.t, err)
	v.dialerPk = cpk
	v.dialerSk = p.PrivKey

	w := ggio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: v.newDialResponseOK(s.Conn().RemoteMultiaddr(), n),
	}

	w.WriteMsg(&res)
}

func (v *mockAutoNatService) sayPublicInvalidSignature(s network.Stream) {
	defer s.Close()
	// read nonce
	n := v.readNonce(s)

	// dial
	require.NoError(v.t, v.dialer.Connect(v.ctx, peer.AddrInfo{s.Conn().RemotePeer(), []ma.Multiaddr{s.Conn().RemoteMultiaddr()}}),
		"AutoNat dialer failed to dial back to the client host")

	// send OK response with signature over an invalid nonce
	w := ggio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: v.newDialResponseOK(s.Conn().RemoteMultiaddr(), n+1),
	}
	w.WriteMsg(&res)
}

func (v *mockAutoNatService) sayPrivate(s network.Stream) {
	defer s.Close()

	// read nonce
	n := v.readNonce(s)

	// dial
	require.NoError(v.t, v.dialer.Connect(v.ctx, peer.AddrInfo{s.Conn().RemotePeer(), []ma.Multiaddr{s.Conn().RemoteMultiaddr()}}),
		"AutoNat dialer failed to dial back to the client host")

	w := ggio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: v.newDialResponseError(pb.Message_E_DIAL_ERROR, n, "no dialable addresses"),
	}
	w.WriteMsg(&res)
}

func (v *mockAutoNatService) sayPublic(s network.Stream) {
	defer s.Close()

	// read nonce
	n := v.readNonce(s)

	// dial first
	require.NoError(v.t, v.dialer.Connect(v.ctx, peer.AddrInfo{s.Conn().RemotePeer(), []ma.Multiaddr{s.Conn().RemoteMultiaddr()}}),
		"AutoNat dialer failed to dial back to the client host")

	w := ggio.NewDelimitedWriter(s)
	res := pb.Message{
		Type:         pb.Message_DIAL_RESPONSE.Enum(),
		DialResponse: v.newDialResponseOK(s.Conn().RemoteMultiaddr(), n),
	}
	w.WriteMsg(&res)
}

func (v *mockAutoNatService) newDialResponseOK(addr ma.Multiaddr, nonce uint64) *pb.Message_DialResponse {
	dr := new(pb.Message_DialResponse)
	dr.Status = pb.Message_OK.Enum()
	dr.Addr = addr.Bytes()
	dr.DialerIdentityCertificate = v.newDialerCertificate(nonce)

	return dr
}

func (v *mockAutoNatService) newDialResponseError(status pb.Message_ResponseStatus, nonce uint64, text string) *pb.Message_DialResponse {
	dr := new(pb.Message_DialResponse)
	dr.Status = status.Enum()
	dr.StatusText = &text
	dr.DialerIdentityCertificate = v.newDialerCertificate(nonce)
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

func connect(t *testing.T, ctx context.Context, a, b host.Host) {
	pinfo := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()}
	err := b.Connect(ctx, pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

func TestServerAttacks(t *testing.T) {

	tsts := map[string]struct {
		streamFnc func(svc *mockAutoNatService)
	}{
		"Signature of an incorrect nonce": {
			streamFnc: func(svc *mockAutoNatService) {
				svc.h.SetStreamHandler(AutoNATProto, svc.sayPublicInvalidSignature)
			}},
		"No Actual Dial back": {
			streamFnc: func(svc *mockAutoNatService) {
				svc.h.SetStreamHandler(AutoNATProto, svc.sayPublicNoDial)
			},
		},
		"Public Key does not match dialer peerId": {
			streamFnc: func(svc *mockAutoNatService) {
				svc.h.SetStreamHandler(AutoNATProto, svc.sayPublicInvalidPublicKey)
			},
		},
	}

	for k, ts := range tsts {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		svc := mkMockAutoNatService(ctx, t)
		ts.streamFnc(svc)
		hc, an := makeAutoNAT(ctx, t, svc.h)
		status := an.Status()
		if status != NATStatusUnknown {
			t.Fatalf("unexpected NAT status: %d, test case is %s", status, k)
		}

		connect(t, ctx, svc.h, hc)
		time.Sleep(2 * time.Second)

		status = an.Status()
		if status != NATStatusUnknown {
			t.Fatalf("unexpected NAT status: %d, test case is \"%s\"", status, k)
		}
	}
}

// tests
func TestAutoNATPrivate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := mkMockAutoNatService(ctx, t)
	svc.h.SetStreamHandler(AutoNATProto, svc.sayPrivate)

	hc, an := makeAutoNAT(ctx, t, svc.h)

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalRoutabilityPrivate{})
	defer s.Close()
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPrivate, err=%s", err)
	}

	status := an.Status()
	if status != NATStatusUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, ctx, svc.h, hc)
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

	svc := mkMockAutoNatService(ctx, t)
	svc.h.SetStreamHandler(AutoNATProto, svc.sayPublic)
	hc, an := makeAutoNAT(ctx, t, svc.h)

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalRoutabilityPublic{})
	defer s.Close()
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPublic, err=%s", err)
	}

	status := an.Status()
	if status != NATStatusUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, ctx, svc.h, hc)
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

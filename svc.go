package autonat

import (
	"context"
	"sync"
	"time"

	pb "github.com/libp2p/go-libp2p-autonat/pb"

	ggio "github.com/gogo/protobuf/io"
	libp2p "github.com/libp2p/go-libp2p"
	host "github.com/libp2p/go-libp2p-host"
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

const P_CIRCUIT = 290

var (
	AutoNATServiceDialTimeout   = 42 * time.Second
	AutoNATServiceResetInterval = 1 * time.Minute
)

// AutoNATService provides NAT autodetection services to other peers
type AutoNATService struct {
	ctx    context.Context
	dialer host.Host

	mx    sync.Mutex
	peers map[peer.ID]struct{}
}

// NewAutoNATService creates a new AutoNATService instance attached to a host
func NewAutoNATService(ctx context.Context, h host.Host, opts ...libp2p.Option) (*AutoNATService, error) {
	opts = append(opts, libp2p.NoListenAddrs)
	dialer, err := libp2p.New(ctx, opts...)
	if err != nil {
		return nil, err
	}

	as := &AutoNATService{
		ctx:    ctx,
		dialer: dialer,
		peers:  make(map[peer.ID]struct{}),
	}
	h.SetStreamHandler(AutoNATProto, as.handleStream)

	go as.resetPeers()

	return as, nil
}

func (as *AutoNATService) handleStream(s inet.Stream) {
	defer s.Close()

	pid := s.Conn().RemotePeer()
	log.Debugf("New stream from %s", pid.Pretty())

	r := ggio.NewDelimitedReader(s, inet.MessageSizeMax)
	w := ggio.NewDelimitedWriter(s)

	var req pb.Message
	var res pb.Message

	err := r.ReadMsg(&req)
	if err != nil {
		log.Debugf("Error reading message from %s: %s", pid.Pretty(), err.Error())
		s.Reset()
		return
	}

	t := req.GetType()
	if t != pb.Message_DIAL {
		log.Debugf("Unexpected message from %s: %s (%d)", pid.Pretty(), t.String(), t)
		s.Reset()
		return
	}

	dr := as.handleDial(pid, req.GetDial().GetPeer())
	res.Type = pb.Message_DIAL_RESPONSE.Enum()
	res.DialResponse = dr

	err = w.WriteMsg(&res)
	if err != nil {
		log.Debugf("Error writing response to %s: %s", pid.Pretty(), err.Error())
		s.Reset()
		return
	}
}

func (as *AutoNATService) handleDial(p peer.ID, mpi *pb.Message_PeerInfo) *pb.Message_DialResponse {
	if mpi == nil {
		return newDialResponseError(pb.Message_E_BAD_REQUEST, "missing peer info")
	}

	mpid := mpi.GetId()
	if mpid != nil {
		mp, err := peer.IDFromBytes(mpid)
		if err != nil {
			return newDialResponseError(pb.Message_E_BAD_REQUEST, "bad peer id")
		}

		if mp != p {
			return newDialResponseError(pb.Message_E_BAD_REQUEST, "peer id mismatch")
		}
	}

	addrs := make([]ma.Multiaddr, 0)
	for _, maddr := range mpi.GetAddrs() {
		addr, err := ma.NewMultiaddrBytes(maddr)
		if err != nil {
			log.Debugf("Error parsing multiaddr: %s", err.Error())
			continue
		}

		// skip relay addresses
		_, err = addr.ValueForProtocol(P_CIRCUIT)
		if err == nil {
			continue
		}

		// skip private network (unroutable) addresses
		if !isPublicAddr(addr) {
			continue
		}

		addrs = append(addrs, addr)
	}

	if len(addrs) == 0 {
		return newDialResponseError(pb.Message_E_DIAL_ERROR, "no dialable addresses")
	}

	return as.doDial(pstore.PeerInfo{ID: p, Addrs: addrs})
}

func (as *AutoNATService) doDial(pi pstore.PeerInfo) *pb.Message_DialResponse {
	// rate limit check
	as.mx.Lock()
	_, ok := as.peers[pi.ID]
	if ok {
		as.mx.Unlock()
		return newDialResponseError(pb.Message_E_DIAL_REFUSED, "too many dials")
	}
	as.peers[pi.ID] = struct{}{}
	as.mx.Unlock()

	ctx, cancel := context.WithTimeout(as.ctx, AutoNATServiceDialTimeout)
	defer cancel()

	err := as.dialer.Connect(ctx, pi)
	if err != nil {
		log.Debugf("error dialing %s: %s", pi.ID.Pretty(), err.Error())
		// wait for the context to timeout to avoid leaking timing information
		// this renders the service ineffective as a port scanner
		<-ctx.Done()
		return newDialResponseError(pb.Message_E_DIAL_ERROR, "dial failed")
	}

	conns := as.dialer.Network().ConnsToPeer(pi.ID)
	if len(conns) == 0 {
		log.Errorf("supposedly connected to %s, but no connection to peer", pi.ID.Pretty())
		return newDialResponseError(pb.Message_E_INTERNAL_ERROR, "internal service error")
	}

	ra := conns[0].RemoteMultiaddr()
	as.dialer.Network().ClosePeer(pi.ID)
	return newDialResponseOK(ra)
}

func (as *AutoNATService) resetPeers() {
	ticker := time.NewTicker(AutoNATServiceResetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			as.mx.Lock()
			as.peers = make(map[peer.ID]struct{})
			as.mx.Unlock()

		case <-as.ctx.Done():
			return
		}
	}
}

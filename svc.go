package autonat

import (
	"context"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"

	pb "github.com/libp2p/go-libp2p-autonat/pb"

	ggio "github.com/gogo/protobuf/io"
	autonat "github.com/libp2p/go-libp2p-autonat"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

const P_CIRCUIT = 290

var (
	// AutoNATServiceDialTimeout defines how long to wait for connection
	// attempts before failing.
	AutoNATServiceDialTimeout = 15 * time.Second
	// AutoNATServiceResetInterval defines how often to reset throttling.
	AutoNATServiceResetInterval = 1 * time.Minute
	// AutoNATServiceResetJitter defines the amplitude of randomness in throttle
	// reset timing.
	AutoNATServiceResetJitter = 15 * time.Second

	// AutoNATServiceThrottle defines how many times each ResetInterval a peer
	// can ask for its autonat address.
	AutoNATServiceThrottle = 3
	// AutoNATGlobalThrottle defines how many total autonat requests this
	// service will answer each ResetInterval.
	AutoNATGlobalThrottle = 30
	// AutoNATMaxPeerAddresses defines maximum number of addreses the autonat
	// service will consider when attempting to connect to the peer.
	AutoNATMaxPeerAddresses = 16
)

// AutoNATService provides NAT autodetection services to other peers
type AutoNATService struct {
	ctx    context.Context
	h      host.Host
	dialer host.Host

	// rate limiter
	mx           sync.Mutex
	reqs         map[peer.ID]int
	globalReqMax int
	globalReqs   int
}

// NewAutoNATService creates a new AutoNATService instance attached to a host
func NewAutoNATService(ctx context.Context, h host.Host, forceEnabled bool, opts ...libp2p.Option) (*AutoNATService, error) {
	opts = append(opts, libp2p.NoListenAddrs)
	dialer, err := libp2p.New(ctx, opts...)
	if err != nil {
		return nil, err
	}

	as := &AutoNATService{
		ctx:          ctx,
		h:            h,
		dialer:       dialer,
		globalReqMax: AutoNATGlobalThrottle,
		reqs:         make(map[peer.ID]int),
	}

	if forceEnabled {
		as.globalReqMax = 0
		h.SetStreamHandler(autonat.AutoNATProto, as.handleStream)
		go as.resetRateLimiter()
	} else {
		go as.enableWhenPublic()
	}

	return as, nil
}

func (as *AutoNATService) handleStream(s network.Stream) {
	defer helpers.FullClose(s)

	pid := s.Conn().RemotePeer()
	log.Debugf("New stream from %s", pid.Pretty())

	r := ggio.NewDelimitedReader(s, network.MessageSizeMax)
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

	dr := as.handleDial(pid, s.Conn().RemoteMultiaddr(), req.GetDial().GetPeer())
	res.Type = pb.Message_DIAL_RESPONSE.Enum()
	res.DialResponse = dr

	err = w.WriteMsg(&res)
	if err != nil {
		log.Debugf("Error writing response to %s: %s", pid.Pretty(), err.Error())
		s.Reset()
		return
	}
}

func (as *AutoNATService) handleDial(p peer.ID, obsaddr ma.Multiaddr, mpi *pb.Message_PeerInfo) *pb.Message_DialResponse {
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

	addrs := make([]ma.Multiaddr, 0, AutoNATMaxPeerAddresses)
	seen := make(map[string]struct{})

	// add observed addr to the list of addresses to dial
	var obsHost net.IP
	if !as.skipDial(obsaddr) {
		addrs = append(addrs, obsaddr)
		seen[obsaddr.String()] = struct{}{}
		obsHost, _ = manet.ToIP(obsaddr)
	}

	for _, maddr := range mpi.GetAddrs() {
		addr, err := ma.NewMultiaddrBytes(maddr)
		if err != nil {
			log.Debugf("Error parsing multiaddr: %s", err.Error())
			continue
		}

		if as.skipDial(addr) {
			continue
		}

		if ip, err := manet.ToIP(addr); err != nil || !obsHost.Equal(ip) {
			continue
		}

		str := addr.String()
		_, ok := seen[str]
		if ok {
			continue
		}

		addrs = append(addrs, addr)
		seen[str] = struct{}{}

		if len(addrs) >= AutoNATMaxPeerAddresses {
			break
		}
	}

	if len(addrs) == 0 {
		return newDialResponseError(pb.Message_E_DIAL_ERROR, "no dialable addresses")
	}

	return as.doDial(peer.AddrInfo{ID: p, Addrs: addrs})
}

func (as *AutoNATService) skipDial(addr ma.Multiaddr) bool {
	// skip relay addresses
	_, err := addr.ValueForProtocol(P_CIRCUIT)
	if err == nil {
		return true
	}

	// skip private network (unroutable) addresses
	if !manet.IsPublicAddr(addr) {
		return true
	}

	// Skip dialing addresses we believe are the local node's
	for _, localAddr := range as.h.Addrs() {
		if localAddr.Equal(addr) {
			return true
		}
	}

	return false
}

func (as *AutoNATService) doDial(pi peer.AddrInfo) *pb.Message_DialResponse {
	// rate limit check
	as.mx.Lock()
	count := as.reqs[pi.ID]
	if count >= AutoNATServiceThrottle || (as.globalReqMax > 0 && as.globalReqs >= as.globalReqMax) {
		as.mx.Unlock()
		return newDialResponseError(pb.Message_E_DIAL_REFUSED, "too many dials")
	}
	as.reqs[pi.ID] = count + 1
	as.globalReqs++
	as.mx.Unlock()

	ctx, cancel := context.WithTimeout(as.ctx, AutoNATServiceDialTimeout)
	defer cancel()

	as.dialer.Peerstore().ClearAddrs(pi.ID)

	as.dialer.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.TempAddrTTL)
	conn, err := as.dialer.Network().DialPeer(ctx, pi.ID)
	if err != nil {
		log.Debugf("error dialing %s: %s", pi.ID.Pretty(), err.Error())
		// wait for the context to timeout to avoid leaking timing information
		// this renders the service ineffective as a port scanner
		<-ctx.Done()
		return newDialResponseError(pb.Message_E_DIAL_ERROR, "dial failed")
	}

	ra := conn.RemoteMultiaddr()
	as.dialer.Network().ClosePeer(pi.ID)
	return newDialResponseOK(ra)
}

func (as *AutoNATService) enableWhenPublic() {
	sub, _ := as.h.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	defer sub.Close()

	running := false

	for {
		select {
		case ev, ok := <-sub.Out():
			if !ok {
				return
			}
			state := ev.(event.EvtLocalReachabilityChanged).Reachability
			if state == network.ReachabilityPublic {
				as.h.SetStreamHandler(autonat.AutoNATProto, as.handleStream)
				if !running {
					go as.resetRateLimiter()
					running = true
				}
			} else {
				as.h.RemoveStreamHandler(autonat.AutoNATProto)
			}
		case <-as.ctx.Done():
			return
		}
	}
}

func (as *AutoNATService) resetRateLimiter() {
	timer := time.NewTimer(AutoNATServiceResetInterval)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			as.mx.Lock()
			as.reqs = make(map[peer.ID]int)
			as.globalReqs = 0
			as.mx.Unlock()
			jitter := rand.Float32() * float32(AutoNATServiceResetJitter)
			timer.Reset(AutoNATServiceResetInterval + time.Duration(int64(jitter)))
		case <-as.ctx.Done():
			return
		}
	}
}

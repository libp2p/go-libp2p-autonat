package autonat

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/transport"

	pb "github.com/libp2p/go-libp2p-autonat/pb"

	"github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// AutoNATService provides NAT autodetection services to other peers
type autoNATService struct {
	ctx          context.Context
	instance     context.CancelFunc
	instanceLock sync.Mutex

	config *config

	// rate limiter
	running    uint32
	mx         sync.Mutex
	reqs       map[peer.ID]int
	globalReqs int

	transports []transport.Transport
}

// NewAutoNATService creates a new AutoNATService instance attached to a host
func newAutoNATService(ctx context.Context, c *config) (*autoNATService, error) {
	if len(c.transports) == 0 {
		return nil, errors.New("Cannot create NAT service without transports")
	}

	as := &autoNATService{
		ctx:    ctx,
		config: c,
		reqs:   make(map[peer.ID]int),
	}

	as.transports = append(as.transports, c.transports...)
	return as, nil
}

func (as *autoNATService) handleStream(s network.Stream) {
	defer s.Close()

	pid := s.Conn().RemotePeer()
	log.Debugf("New stream from %s", pid.Pretty())

	r := protoio.NewDelimitedReader(s, network.MessageSizeMax)
	w := protoio.NewDelimitedWriter(s)

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

func (as *autoNATService) handleDial(p peer.ID, obsaddr ma.Multiaddr, mpi *pb.Message_PeerInfo) *pb.Message_DialResponse {
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

	addrs := make([]ma.Multiaddr, 0, as.config.maxPeerAddresses)
	seen := make(map[string]struct{})

	// add observed addr to the list of addresses to dial
	var obsHost net.IP
	if !as.config.dialPolicy.skipDial(obsaddr) {
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

		if as.config.dialPolicy.skipDial(addr) {
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

		if len(addrs) >= as.config.maxPeerAddresses {
			break
		}
	}

	if len(addrs) == 0 {
		return newDialResponseError(pb.Message_E_DIAL_ERROR, "no dialable addresses")
	}

	return as.doDial(peer.AddrInfo{ID: p, Addrs: addrs})
}

// We will try to diversify dial attempts across IP addresses and transports.
func (as *autoNATService) doDial(pi peer.AddrInfo) *pb.Message_DialResponse {
	// rate limit check
	as.mx.Lock()
	count := as.reqs[pi.ID]
	if count >= as.config.throttlePeerMax || (as.config.throttleGlobalMax > 0 &&
		as.globalReqs >= as.config.throttleGlobalMax) {
		as.mx.Unlock()
		return newDialResponseError(pb.Message_E_DIAL_REFUSED, "too many dials")
	}
	as.reqs[pi.ID] = count + 1
	as.globalReqs++
	as.mx.Unlock()

	ctx, cancel := context.WithTimeout(as.ctx, as.config.dialTimeout)
	defer cancel()

	successAddrs := make([]ma.Multiaddr, 0, len(pi.Addrs))
	failedAddrs := make([]ma.Multiaddr, 0, len(pi.Addrs))
	nSuccessDials := 0

	// The maximum number of addresses we dial is bound by config.maxPeerAddresses as
	// we don't pass more than that number of addresses to this function.
ADDRLOOP:
	for _, addr := range pi.Addrs {
		for _, t := range as.transports {
			if t.CanDial(addr) {
				cc, err := t.Dial(ctx, addr, pi.ID)
				if err == nil {
					successAddrs = append(successAddrs, addr)

					if err := cc.Close(); err != nil {
						log.Warnf("failed to close opened connection: %w", err)
					}

					// we've seen enough successful dials, we are done.
					nSuccessDials++
					if nSuccessDials >= as.config.maxSuccessfulDials {
						break ADDRLOOP
					}
				} else {
					log.Debugf("error dialing %s on address %s: %s", pi.ID.Pretty(), addr, err.Error())
					failedAddrs = append(failedAddrs, addr)
				}
				break
			}
		}
	}

	// TODO How to make this ineffective as a port scanner now ?
	return newDialResponseOK(successAddrs, failedAddrs)
}

// Enable the autoNAT service if it is not running.
func (as *autoNATService) Enable() {
	as.instanceLock.Lock()
	defer as.instanceLock.Unlock()
	if as.instance != nil {
		return
	}
	inst, cncl := context.WithCancel(as.ctx)
	as.instance = cncl

	go as.background(inst)
}

// Disable the autoNAT service if it is running.
func (as *autoNATService) Disable() {
	as.instanceLock.Lock()
	defer as.instanceLock.Unlock()
	if as.instance != nil {
		as.instance()
		as.instance = nil
	}
}

func (as *autoNATService) background(ctx context.Context) {
	as.config.host.SetStreamHandler(AutoNATProto, as.handleStream)

	timer := time.NewTimer(as.config.throttleResetPeriod)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			as.mx.Lock()
			as.reqs = make(map[peer.ID]int)
			as.globalReqs = 0
			as.mx.Unlock()
			jitter := rand.Float32() * float32(as.config.throttleResetJitter)
			timer.Reset(as.config.throttleResetPeriod + time.Duration(int64(jitter)))
		case <-ctx.Done():
			as.config.host.RemoveStreamHandler(AutoNATProto)
			return
		}
	}
}

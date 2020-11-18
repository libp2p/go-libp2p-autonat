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

	// START DIALING
	// 1. Aim is to diversify dial attempts across address groups where the grouping key is (IP address + transport protocol(port dosen't matter))
	// 2. We will continue dialing addresses in a group till we see a successful dial for an address.
	// 3. If we see a successful address, we move on to the next group.
	// 4. If we never see a success, we will exhaust all the addresses in a group and not visit it again.
	// 5. Keep cycling through the groups till we either exhaust all addresses or see enough successful dials.
	successAddrs := make([]ma.Multiaddr, 0, len(pi.Addrs))
	failedAddrs := make([]ma.Multiaddr, 0, len(pi.Addrs))
	nsuccessDials := 0
	ntotalDials := 0

	addrGroups := make(map[string][]ma.Multiaddr)
	for _, a := range pi.Addrs {
		group := groupKey(a)
		addrGroups[group] = append(addrGroups[group], a)
	}

MAINLOOP:
	for {
		for group := range addrGroups {
			dialSuccess := false
		ADDRLOOP:
			for i, addr := range addrGroups[group] {

				for _, t := range as.transports {
					if t.CanDial(addr) {
						cc, err := t.Dial(ctx, addr, pi.ID)
						if err == nil {
							// TODO Is this a reasonable check ?
							if cc.RemoteMultiaddr().Equal(addr) {
								successAddrs = append(successAddrs, addr)
							}
							if err := cc.Close(); err != nil {
								log.Warnf("failed to close opened connection: %w", err)
							}

							// we've seen enough successful dials, we are done.
							nsuccessDials++
							if nsuccessDials >= as.config.maxSuccessfulDials {
								break MAINLOOP
							}
							dialSuccess = true
						} else {
							log.Debugf("error dialing %s on address %s: %s", pi.ID.Pretty(), addr, err.Error())
							failedAddrs = append(failedAddrs, addr)
						}
						break
					}
				}
				ntotalDials++
				// we've exhausted all addresses, we are done.
				if ntotalDials >= len(pi.Addrs) {
					break MAINLOOP
				}
				// we saw a success, let's change the yet to be dialed addresses accordingly and move on
				// to the next group
				if dialSuccess {
					// if we've exhausted all addresses, just remove the group
					if i >= len(addrGroups[group])-1 {
						addrGroups[group] = nil
					} else {
						addrGroups[group] = addrGroups[group][i+1:]
					}
					break ADDRLOOP
				}
			}

			if !dialSuccess {
				// we went through all addresses for the group without seeing a success
				// just remove the group and move on
				addrGroups[group] = nil
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

// GroupKey returns the group in which this address belongs. Currently, an
// address's group is just the address with all ports set to 0.
func groupKey(addr ma.Multiaddr) string {
	key := make([]byte, 0, len(addr.Bytes()))
	ma.ForEach(addr, func(c ma.Component) bool {
		switch proto := c.Protocol(); proto.Code {
		case ma.P_TCP, ma.P_UDP:
			key = append(key, proto.VCode...)
			key = append(key, 0, 0) // zero in two bytes
		default:
			key = append(key, c.Bytes()...)
		}
		return true
	})

	return string(key)
}

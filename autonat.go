package autonat

import (
	"context"
	"errors"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

// NATStatus is the state of NAT as detected by the ambient service.
type NATStatus int

const (
	// NATStatusUnknown means that the ambient service has not been
	// able to decide the presence of NAT in the most recent attempt to test
	// dial through known autonat peers.  initial state.
	NATStatusUnknown NATStatus = iota
	// NATStatusPublic means this node believes it is externally dialable
	NATStatusPublic
	// NATStatusPrivate means this node believes it is behind a NAT
	NATStatusPrivate
)

var (
	AutoNATBootDelay       = 15 * time.Second
	AutoNATRetryInterval   = 90 * time.Second
	AutoNATRefreshInterval = 15 * time.Minute
	AutoNATRequestTimeout  = 30 * time.Second
)

// AutoNAT is the interface for ambient NAT autodiscovery
type AutoNAT interface {
	// Status returns the current NAT status
	Status() NATStatus
	// PublicAddr returns the public dial address when NAT status is public and an
	// error otherwise
	PublicAddr() (ma.Multiaddr, error)
}

// AmbientAutoNAT is the implementation of ambient NAT autodiscovery
type AmbientAutoNAT struct {
	ctx  context.Context
	host host.Host

	candidatePeers chan network.Conn
	observations   chan autoNATResult
	status         atomic.Value
	// Reflects the confidence on of the NATStatus being private, as a single
	// dialback may fail for reasons unrelated to NAT.
	// If it is <3, then multiple autoNAT peers may be contacted for dialback
	// If only a single autoNAT peer is known, then the confidence increases
	// for each failure until it reaches 3.
	confidence  int
	lastInbound time.Time
	lastProbe   time.Time

	emitUnknown event.Emitter
	emitPublic  event.Emitter
	emitPrivate event.Emitter
}

type autoNATResult struct {
	NATStatus
	address ma.Multiaddr
}

// NewAutoNAT creates a new ambient NAT autodiscovery instance attached to a host
func NewAutoNAT(ctx context.Context, h host.Host) AutoNAT {
	emitUnknown, _ := h.EventBus().Emitter(new(event.EvtLocalRoutabilityUnknown))
	emitPublic, _ := h.EventBus().Emitter(new(event.EvtLocalRoutabilityPublic))
	emitPrivate, _ := h.EventBus().Emitter(new(event.EvtLocalRoutabilityPrivate))

	as := &AmbientAutoNAT{
		ctx:            ctx,
		host:           h,
		candidatePeers: make(chan network.Conn, 5),
		observations:   make(chan autoNATResult, 1),

		emitUnknown: emitUnknown,
		emitPublic:  emitPublic,
		emitPrivate: emitPrivate,
	}
	as.status.Store(autoNATResult{NATStatusUnknown, nil})

	h.Network().Notify(as)
	go as.background()

	return as
}

// Status returns the AutoNAT observed reachability status.
func (as *AmbientAutoNAT) Status() NATStatus {
	s := as.status.Load().(autoNATResult)
	return s.NATStatus
}

func (as *AmbientAutoNAT) emitStatus() {
	status := as.status.Load().(autoNATResult)
	switch status.NATStatus {
	case NATStatusUnknown:
		as.emitUnknown.Emit(event.EvtLocalRoutabilityUnknown{})
	case NATStatusPublic:
		as.emitPublic.Emit(event.EvtLocalRoutabilityPublic{})
	case NATStatusPrivate:
		as.emitPrivate.Emit(event.EvtLocalRoutabilityPrivate{})
	}
}

// PublicAddr returns the publicly connectable Multiaddr of this node if one is known.
func (as *AmbientAutoNAT) PublicAddr() (ma.Multiaddr, error) {
	s := as.status.Load().(autoNATResult)
	if s.NATStatus != NATStatusPublic {
		return nil, errors.New("NAT Status is not public")
	}

	return s.address, nil
}

func (as *AmbientAutoNAT) background() {
	// wait a bit for the node to come online and establish some connections
	// before starting autodetection
	delay := AutoNATBootDelay
	for {
		select {
		// new connection occured.
		case conn := <-as.candidatePeers:
			if conn.Stat().Direction == network.DirInbound && manet.IsPublicAddr(conn.RemoteMultiaddr()) {
				as.lastInbound = time.Now()
			}
		// TODO: network changed.

		// probe finished.
		case result := <-as.observations:
			as.recordObservation(result)
		case <-time.After(delay):
		case <-as.ctx.Done():
			return
		}

		delay = as.scheduleProbe()
	}
}

// scheduleProbe calculates when the next probe should be scheduled for,
// and launches it if that time is now.
func (as *AmbientAutoNAT) scheduleProbe() time.Duration {
	// Our baseline is a probe every 'AutoNATRefreshInterval'
	// This is modulated by:
	// * recent inbound connections make us willing to wait up to 2x longer between probes.
	// * low confidence makes us speed up between probes.
	fixedNow := time.Now()
	currentStatus := as.status.Load().(autoNATResult)

	nextProbe := fixedNow
	if !as.lastProbe.IsZero() {
		untilNext := AutoNATRefreshInterval
		if currentStatus.NATStatus == NATStatusUnknown {
			untilNext = AutoNATRetryInterval
		} else if currentStatus.NATStatus == NATStatusPublic && as.lastInbound.After(as.lastProbe) {
			untilNext *= 2
		} else if as.confidence < 3 {
			untilNext = AutoNATRetryInterval
		}
		nextProbe = as.lastProbe.Add(untilNext)
	}
	if fixedNow.After(nextProbe) || fixedNow == nextProbe {
		as.lastProbe = fixedNow
		go as.probeNextPeer()
		return AutoNATRetryInterval
	}
	return nextProbe.Sub(fixedNow)
}

// Update the current status based on an observed result.
func (as *AmbientAutoNAT) recordObservation(observation autoNATResult) {
	currentStatus := as.status.Load().(autoNATResult)
	if observation.NATStatus == NATStatusPublic {
		log.Debugf("NAT status is public")
		if currentStatus.NATStatus == NATStatusPrivate {
			// we are flipping our NATStatus, so confidence drops to 0
			as.confidence = 0
		} else if as.confidence < 3 {
			as.confidence++
		}
		if observation.address != nil {
			if currentStatus.address != nil && !observation.address.Equal(currentStatus.address) {
				as.confidence--
			}
			as.status.Store(observation)
		}
		if currentStatus.address != nil || observation.address != nil {
			as.emitStatus()
		}
	} else if observation.NATStatus == NATStatusPrivate {
		log.Debugf("NAT status is private")
		if currentStatus.NATStatus == NATStatusPublic {
			if as.confidence < 1 {
				as.confidence--
			} else {
				// we are flipping our NATStatus, so confidence drops to 0
				as.confidence = 0
				as.status.Store(observation)
				as.emitStatus()
			}
		} else if as.confidence < 3 {
			as.confidence++
			as.status.Store(observation)
			as.emitStatus()
		}
	} else if as.confidence > 0 {
		// don't just flip to unknown, reduce confidence first
		as.confidence--
	} else {
		log.Debugf("NAT status is unknown")
		as.status.Store(autoNATResult{NATStatusUnknown, nil})
		as.emitStatus()
	}
}

func (as *AmbientAutoNAT) probe(pi *peer.AddrInfo) {
	cli := NewAutoNATClient(as.host)
	ctx, cancel := context.WithTimeout(as.ctx, AutoNATRequestTimeout)
	defer cancel()

	as.host.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.TempAddrTTL)
	a, err := cli.DialBack(ctx, pi.ID)

	switch {
	case err == nil:
		log.Debugf("Dialback through %s successful; public address is %s", pi.ID.Pretty(), a.String())
		as.observations <- autoNATResult{NATStatusPublic, a}
	case IsDialError(err):
		log.Debugf("Dialback through %s failed", pi.ID.Pretty())
		as.observations <- autoNATResult{NATStatusPrivate, nil}
	default:
		as.observations <- autoNATResult{NATStatusUnknown, nil}
	}
}

func (as *AmbientAutoNAT) probeNextPeer() {
	peers := as.host.Network().Peers()
	if len(peers) == 0 {
		return
	}

	connected := make([]peer.AddrInfo, 0, len(peers))
	others := make([]peer.AddrInfo, 0, len(peers))

	for _, p := range peers {
		info := as.host.Peerstore().PeerInfo(p)
		if as.host.Network().Connectedness(p) == network.Connected {
			connected = append(connected, info)
		} else {
			others = append(others, info)
		}
	}
	// TODO: track and exclude recently probed peers.

	shufflePeers(connected)

	if len(connected) > 0 {
		as.probe(&connected[0])
		return
	} else if len(others) > 0 {
		shufflePeers(others)
		as.probe(&others[0])
	}
}

func shufflePeers(peers []peer.AddrInfo) {
	for i := range peers {
		j := rand.Intn(i + 1)
		peers[i], peers[j] = peers[j], peers[i]
	}
}

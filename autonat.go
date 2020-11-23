package autonat

import (
	"context"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p-autonat/addrset"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"golang.org/x/xerrors"

	logging "github.com/ipfs/go-log"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var log = logging.Logger("autonat")

// AmbientAutoNAT is the implementation of ambient NAT autodiscovery
type AmbientAutoNAT struct {
	ctx  context.Context
	host host.Host

	*config

	inboundConn  chan network.Conn
	observations chan autoNATResult
	lastInbound  time.Time
	lastProbeTry time.Time
	lastProbe    time.Time
	recentProbes map[peer.ID]time.Time

	service *autoNATService

	emitReachabilityChanged event.Emitter
	subscriber              event.Subscription

	addrsSet *addrset.DialAddrsSet
	// returns addresses that have been confirmed to be public by the AutoNAT server
	publicAddrsManager *dialedAddrsManager
	// returns addresses that have been confirmed to be private by the AutoNAT server
	privateAddrsManager *dialedAddrsManager

	// current NAT status and known dialable addresses.
	status atomic.Value
}

// StaticAutoNAT is a simple AutoNAT implementation when a single NAT status is desired.
type StaticAutoNAT struct {
	ctx          context.Context
	host         host.Host
	reachability network.Reachability
	service      *autoNATService
	addrs        []ma.Multiaddr
}

type natStatus struct {
	reachability network.Reachability
	publicAddrs  []ma.Multiaddr
}

type autoNATResult struct {
	// for each address that we sent to the server, we map it's reachability
	addrReachability map[ma.Multiaddr]network.Reachability
	observer         ma.Multiaddr
}

// New creates a new NAT autodiscovery system attached to a host
func New(ctx context.Context, h host.Host, options ...Option) (AutoNAT, error) {
	var err error
	conf := new(config)
	conf.host = h
	conf.dialPolicy.host = h

	if err = defaults(conf); err != nil {
		return nil, err
	}

	for _, o := range options {
		if err = o(conf); err != nil {
			return nil, err
		}
	}
	emitReachabilityChanged, _ := h.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)

	var service *autoNATService
	if (!conf.forceReachability || conf.reachability == network.ReachabilityPublic) && len(conf.transports) != 0 {
		service, err = newAutoNATService(ctx, conf)
		if err != nil {
			return nil, err
		}
		service.Enable()
	}

	if conf.forceReachability {
		emitReachabilityChanged.Emit(event.EvtLocalReachabilityChanged{Reachability: conf.reachability})

		if conf.reachability == network.ReachabilityPublic && len(conf.dialAddrs) == 0 {
			return nil, xerrors.New("dial addrs must be configured if forced reachability is public")
		}

		return &StaticAutoNAT{
			ctx:          ctx,
			host:         h,
			reachability: conf.reachability,
			addrs:        conf.dialAddrs,
			service:      service,
		}, nil
	}

	as := &AmbientAutoNAT{
		ctx:          ctx,
		host:         h,
		config:       conf,
		inboundConn:  make(chan network.Conn, 5),
		observations: make(chan autoNATResult, 1),

		emitReachabilityChanged: emitReachabilityChanged,
		service:                 service,
		recentProbes:            make(map[peer.ID]time.Time),

		addrsSet:            addrset.NewDialAddrsSet(),
		publicAddrsManager:  newDialAddrManager(),
		privateAddrsManager: newDialAddrManager(),
	}

	as.status.Store(natStatus{network.ReachabilityUnknown, nil})
	subscriber, err := as.host.EventBus().Subscribe([]interface{}{new(event.EvtPeerIdentificationCompleted),
		new(event.EvtDiscoveredAddresses), eventbus.BufSize(256)})
	if err != nil {
		return nil, err
	}
	as.subscriber = subscriber

	h.Network().Notify(as)
	go as.background()

	return as, nil
}

// Status returns the AutoNAT observed reachability status.
func (as *AmbientAutoNAT) Status() network.Reachability {
	s := as.status.Load().(natStatus)
	return s.reachability
}

func (as *AmbientAutoNAT) emitStatus() {
	status := as.status.Load().(natStatus)
	as.emitReachabilityChanged.Emit(event.EvtLocalReachabilityChanged{Reachability: status.reachability})
}

// PublicAddr returns the publicly dialable Multiaddrs of this node.
func (as *AmbientAutoNAT) PublicAddrs() ([]ma.Multiaddr, error) {
	s := as.status.Load().(natStatus)
	if len(s.publicAddrs) != 0 {
		return s.publicAddrs, nil
	}

	// return all non-private addresses
	return as.addrsSet.GetAddrsInStates(addrset.Confirmed, addrset.UnconfirmedExternalUnknown, addrset.UnconfirmedListenUnknown), nil
}

func ipInList(candidate ma.Multiaddr, list []ma.Multiaddr) bool {
	candidateIP, _ := manet.ToIP(candidate)
	for _, i := range list {
		if ip, err := manet.ToIP(i); err == nil && ip.Equal(candidateIP) {
			return true
		}
	}
	return false
}

func (as *AmbientAutoNAT) background() {
	// wait a bit for the node to come online and establish some connections
	// before starting autodetection
	delay := as.config.bootDelay

	subChan := as.subscriber.Out()
	defer as.subscriber.Close()
	defer as.emitReachabilityChanged.Close()

	timer := time.NewTimer(delay)
	defer timer.Stop()
	timerRunning := true

	for {
		select {
		// new inbound connection.
		case conn := <-as.inboundConn:
			localAddrs := as.host.Addrs()
			ca := as.status.Load().(natStatus)
			if ca.publicAddrs != nil {
				localAddrs = append(localAddrs, ca.publicAddrs...)
			}
			if manet.IsPublicAddr(conn.RemoteMultiaddr()) &&
				!ipInList(conn.RemoteMultiaddr(), localAddrs) {
				as.lastInbound = time.Now()
			}

		case e := <-subChan:
			switch e := e.(type) {
			case event.EvtPeerIdentificationCompleted:
				if s, err := as.host.Peerstore().SupportsProtocols(e.Peer, AutoNATProto); err == nil && len(s) > 0 {
					currentStatus := as.status.Load().(natStatus)
					if currentStatus.reachability == network.ReachabilityUnknown {
						as.tryProbe(e.Peer)
					}
				}

			case event.EvtDiscoveredAddresses:
				switch e.Source {
				case event.IdentifyObserved:
					for _, a := range e.Addresses {
						// we only add observed addresses if we don't already know about them.
						// there's no reason to mutate the state of an existing address on it being "re-observed".
						if as.addrsSet.TryAdd(a) {
							as.addrsSet.SetState(a, addrset.UnconfirmedExternalUnknown)
						}
					}

				case event.InterfaceNewListen:
					for _, a := range e.Addresses {
						if as.addrsSet.TryAdd(a) {
							as.addrsSet.SetState(a, addrset.UnconfirmedListenUnknown)
						} else {
							// if it's a listen addresses we've previously recorded as an observed address, record it as a listen address.
							// otherwise. leave it as is.
							// TODO Can we keep it simple and get rid of mutations like this ?
							curr := as.addrsSet.GetState(a)
							if curr == addrset.UnconfirmedExternalUnknown {
								as.addrsSet.SetState(a, addrset.UnconfirmedListenUnknown)
							}
						}
					}

				case event.NetRouteChange:
					// TODO Should we negate all information now ?
					// i.e. Should we clear out address set and address managers
					// OR Remove all previous listen addresses we've heard of ?

					// event should contain new resolved interface listen addresses
					for _, a := range e.Addresses {
						as.addrsSet.TryAdd(a)
						as.addrsSet.SetState(a, addrset.UnconfirmedListenUnknown)
					}

				case event.NATPnP:
					// TODO How to generate and process this event ?
				}

			default:
				log.Errorf("unknown event type: %T", e)
			}

		// probe finished.
		case result, ok := <-as.observations:
			if !ok {
				return
			}
			as.recordObservation(result)
		case <-timer.C:
			peer := as.getPeerToProbe()
			as.tryProbe(peer)
			timerRunning = false
		case <-as.ctx.Done():
			return
		}

		// Drain the timer channel if it hasn't fired in preparation for Resetting it.
		if timerRunning && !timer.Stop() {
			<-timer.C
		}
		timer.Reset(as.scheduleProbe())
		timerRunning = true
	}
}

func (as *AmbientAutoNAT) cleanupRecentProbes() {
	fixedNow := time.Now()
	for k, v := range as.recentProbes {
		if fixedNow.Sub(v) > as.throttlePeerPeriod {
			delete(as.recentProbes, k)
		}
	}
}

// scheduleProbe calculates when the next probe should be scheduled for.
func (as *AmbientAutoNAT) scheduleProbe() time.Duration {
	// Our baseline is a probe every 'AutoNATRefreshInterval'
	// This is modulated by:
	// * if we are in an unknown state that should drop to 'AutoNATRetryInterval'
	// * recent inbound connections (implying continued connectivity) should decrease the retry when public
	// * recent inbound connections when not public mean we should try more actively to see if we're public.
	fixedNow := time.Now()
	currentStatus := as.status.Load().(natStatus)

	nextProbe := fixedNow
	// Don't look for peers in the peer store more than once per second.
	if !as.lastProbeTry.IsZero() {
		backoff := as.lastProbeTry.Add(time.Second)
		if backoff.After(nextProbe) {
			nextProbe = backoff
		}
	}
	if !as.lastProbe.IsZero() {
		untilNext := as.config.refreshInterval
		if currentStatus.reachability == network.ReachabilityUnknown {
			untilNext = as.config.retryInterval
		} else if currentStatus.reachability == network.ReachabilityPublic && as.lastInbound.After(as.lastProbe) {
			untilNext *= 2
		} else if currentStatus.reachability != network.ReachabilityPublic && as.lastInbound.After(as.lastProbe) {
			untilNext /= 5
		}

		if as.lastProbe.Add(untilNext).After(nextProbe) {
			nextProbe = as.lastProbe.Add(untilNext)
		}
	}

	return nextProbe.Sub(fixedNow)
}

// Update the current status based on an observed result.
func (as *AmbientAutoNAT) recordObservation(observation autoNATResult) {
	// record observation
	for addr, reach := range observation.addrReachability {
		switch reach {
		case network.ReachabilityPublic:
			as.publicAddrsManager.record(observation.observer, addr)

		case network.ReachabilityPrivate:
			as.privateAddrsManager.record(observation.observer, addr)
		}
	}

	// piggy-back gcs on top of recoding as these are NOT heavy operations.
	as.publicAddrsManager.gc()
	as.privateAddrsManager.gc()

	// update information about confirmed addresses
	oldConfirmed := as.addrsSet.GetAddrsInStates(addrset.Confirmed)
	newConfirmed := as.publicAddrsManager.activatedAddrs()
	// remove confirmed addrs from the address set that are no longer confirmed
	oldMap := make(map[ma.Multiaddr]struct{}, len(oldConfirmed))
	for _, a := range oldConfirmed {
		oldMap[a] = struct{}{}
	}
	newMap := make(map[ma.Multiaddr]struct{}, len(newConfirmed))
	for _, a := range newConfirmed {
		newMap[a] = struct{}{}
	}

	for a := range oldMap {
		if _, ok := newMap[a]; !ok {
			as.addrsSet.AssertStateAndRemove(a, addrset.Confirmed)
		}
	}

	// mark the confirmed addresses in the SM.
	for a := range newMap {
		as.addrsSet.TryAdd(a)
		as.addrsSet.SetState(a, addrset.Confirmed)
	}

	// update information about private addresses
	oldPrivate := as.addrsSet.GetAddrsInStates(addrset.Private)
	newPrivate := as.privateAddrsManager.activatedAddrs()

	// remove private addrs from the address set that are no longer private
	oldMap = make(map[ma.Multiaddr]struct{}, len(oldPrivate))
	for _, a := range oldPrivate {
		oldMap[a] = struct{}{}
	}

	newMap = make(map[ma.Multiaddr]struct{}, len(newPrivate))
	for _, a := range newPrivate {
		newMap[a] = struct{}{}
	}

	for a := range oldMap {
		if _, ok := newMap[a]; !ok {
			if as.addrsSet.GetState(a) != addrset.Confirmed {
				as.addrsSet.AssertStateAndRemove(a, addrset.Private)
			}
		}
	}

	for a := range newMap {
		as.addrsSet.TryAdd(a)
		if as.addrsSet.GetState(a) != addrset.Confirmed {
			as.addrsSet.SetState(a, addrset.Private)
		}
	}

	// Emit reachability event if status flips
	currentStatus := as.status.Load().(natStatus)

	var reach network.Reachability
	confirmed := as.addrsSet.GetAddrsInStates(addrset.Confirmed)
	private := as.addrsSet.GetAddrsInStates(addrset.Private)

	if len(confirmed) != 0 {
		reach = network.ReachabilityPublic
	} else if len(private) != 0 {
		reach = network.ReachabilityPrivate
	} else {
		reach = network.ReachabilityUnknown
	}

	if currentStatus.reachability != reach {
		status := natStatus{reachability: reach}
		if reach == network.ReachabilityPublic {
			status.publicAddrs = as.publicAddrsManager.activatedAddrs()
		}
		as.status.Store(status)
		as.emitStatus()
	}
}

func (as *AmbientAutoNAT) tryProbe(p peer.ID) bool {
	as.lastProbeTry = time.Now()
	if p.Validate() != nil {
		return false
	}

	if lastTime, ok := as.recentProbes[p]; ok {
		if time.Since(lastTime) < as.throttlePeerPeriod {
			return false
		}
	}
	as.cleanupRecentProbes()

	info := as.host.Peerstore().PeerInfo(p)

	if !as.config.dialPolicy.skipPeer(info.Addrs) {
		as.recentProbes[p] = time.Now()
		as.lastProbe = time.Now()
		go as.probe(&info)
		return true
	}
	return false
}

func (as *AmbientAutoNAT) probe(pi *peer.AddrInfo) {
	cli := NewAutoNATClient(as.host)
	ctx, cancel := context.WithTimeout(as.ctx, as.config.requestTimeout)
	defer cancel()

	toDial := as.dialAddrs()
	success, failed, observer, err := cli.DialBack(ctx, pi.ID, toDial)
	var result autoNATResult
	result.observer = observer
	addrReachable := make(map[ma.Multiaddr]network.Reachability)

	// if the dial errored out because the server could NOT find any dialable addresses,
	// we mark all addresses as private.
	if err != nil && IsDialError(err) {
		for _, a := range toDial {
			addrReachable[a] = network.ReachabilityPrivate
		}
	} else {
		for _, addr := range success {
			addrReachable[addr] = network.ReachabilityPublic
		}
		for _, addr := range failed {
			addrReachable[addr] = network.ReachabilityPrivate
		}

		results := make(map[ma.Multiaddr]network.Reachability)
		// if the server sent us a success/failure response for an address we sent to it,
		// mark it as such, otherwise mark it as ReachabilityUnknown.
		for _, a := range toDial {
			if r, ok := addrReachable[a]; ok {
				results[a] = r
			} else {
				results[a] = network.ReachabilityUnknown
			}
		}
		result.addrReachability = results
	}

	select {
	case as.observations <- result:
	case <-as.ctx.Done():
		return
	}
}

// Order:
// 1. couple of AutoNAT confirmed addresses at random so we can refresh them in the confirmed address manager.
// 2. AutoNAT Unconfirmed Listen addresses with "unknown" reachability.
// 3. AutoNAT Unconfirmed externally observed addresses(which have been vetted by the obsAddrManager though) with "unknown" reachability.
func (as *AmbientAutoNAT) dialAddrs() []ma.Multiaddr {
	var toDial []ma.Multiaddr

	// confirmed addrs
	confirmed := as.addrsSet.GetAddrsInStates(addrset.Confirmed)
	shuffleAddrs(confirmed)
	n := 2
	if len(confirmed) < n {
		n = len(confirmed)
	}
	toDial = append(toDial, confirmed[:n]...)

	// listen
	toDial = append(toDial, as.addrsSet.GetAddrsInStates(addrset.UnconfirmedListenUnknown)...)

	// external
	toDial = append(toDial, as.addrsSet.GetAddrsInStates(addrset.UnconfirmedExternalUnknown)...)

	return toDial
}

func (as *AmbientAutoNAT) getPeerToProbe() peer.ID {
	peers := as.host.Network().Peers()
	if len(peers) == 0 {
		return ""
	}

	candidates := make([]peer.ID, 0, len(peers))

	for _, p := range peers {
		info := as.host.Peerstore().PeerInfo(p)
		// Exclude peers which don't support the autonat protocol.
		if proto, err := as.host.Peerstore().SupportsProtocols(p, AutoNATProto); len(proto) == 0 || err != nil {
			continue
		}

		// Exclude peers in backoff.
		if lastTime, ok := as.recentProbes[p]; ok {
			if time.Since(lastTime) < as.throttlePeerPeriod {
				continue
			}
		}

		if as.config.dialPolicy.skipPeer(info.Addrs) {
			continue
		}
		candidates = append(candidates, p)
	}

	if len(candidates) == 0 {
		return ""
	}

	shufflePeers(candidates)
	return candidates[0]
}

func shufflePeers(peers []peer.ID) {
	for i := range peers {
		j := rand.Intn(i + 1)
		peers[i], peers[j] = peers[j], peers[i]
	}
}

func shuffleAddrs(addrs []ma.Multiaddr) {
	for i := range addrs {
		j := rand.Intn(i + 1)
		addrs[i], addrs[j] = addrs[j], addrs[i]
	}
}

// Status returns the AutoNAT observed reachability status.
func (s *StaticAutoNAT) Status() network.Reachability {
	return s.reachability
}

// PublicAddr returns the publicly connectable Multiaddr of this node if one is known.
func (s *StaticAutoNAT) PublicAddrs() ([]ma.Multiaddr, error) {
	return s.addrs, nil
}

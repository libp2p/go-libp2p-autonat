package autonat

import (
	"time"

	"github.com/libp2p/go-libp2p-core/peerstore"

	ma "github.com/multiformats/go-multiaddr"
)

// ActivationThresh sets how many times an address must be seen as "activated"
// and therefore marked as confirmed.
// The "seen" events expire by default after 40 minutes
// (OwnObservedAddressTTL * ActivationThreshold). They are cleaned up during
// GC.
var activationThresh = 4

// observation records an address observation from an "observer" (where every IP
// address is a unique observer).
type observation struct {
	// seenTime is the last time this observation was made.
	seenTime time.Time
}

// observedAddr is an entry for an address reported by AutoNAT servers.
// We only use addresses that:
// - have been observed at least 4 times in last 40 minutes.
// - have been observed at least once recently (10 minutes), because our position in the
//   network, or network port mapppings, may have changed.
type observedAddr struct {
	addr       ma.Multiaddr
	seenBy     map[string]observation // peer(observer) address -> observation info
	lastSeen   time.Time
	numInbound int
}

func (oa *observedAddr) activated() bool {

	// We only activate if other peers observed the same address
	// of ours at least 4 times. SeenBy peers are removed by GC if
	// they say the address more than ttl*ActivationThresh
	return len(oa.seenBy) >= activationThresh
}

type newObservation struct {
	observer ma.Multiaddr
	observed ma.Multiaddr
}

// dialedAddrsManager keeps track of a addresses that are dialled by the AutoNAT server.
type dialedAddrsManager struct {
	addrs []*observedAddr
	ttl   time.Duration
}

// newDialAddrManager returns a new address manager using
// peerstore.OwnObservedAddressTTL as the TTL.
func newDialAddrManager() *dialedAddrsManager {
	oas := &dialedAddrsManager{
		ttl: peerstore.OwnObservedAddrTTL,
	}
	return oas
}

// activateAddrs return all activated dialled addresses
func (oas *dialedAddrsManager) activatedAddrs() []ma.Multiaddr {
	now := time.Now()
	if len(oas.addrs) == 0 {
		return nil
	}

	var confirmed []ma.Multiaddr

	for i := range oas.addrs {
		a := oas.addrs[i]
		if now.Sub(a.lastSeen) <= oas.ttl && a.activated() {
			confirmed = append(confirmed, a.addr)
		}
	}

	return confirmed
}

// Record records an address observation, if valid.
// returns true if address is activated because of this observation.
func (oas *dialedAddrsManager) record(observer, observed ma.Multiaddr) bool {
	now := time.Now()
	observerString := observerGroup(observer)
	ob := observation{
		seenTime: now,
	}

	// check if observed address seen yet, if so, update it
	for i := range oas.addrs {
		observedAddr := oas.addrs[i]
		if observedAddr.addr.Equal(observed) {
			observedAddr.seenBy[observerString] = ob
			observedAddr.lastSeen = now
			return observedAddr.activated()
		}
	}

	// observed address not seen yet, append it
	oa := &observedAddr{
		addr: observed,
		seenBy: map[string]observation{
			observerString: ob,
		},
		lastSeen: now,
	}
	oas.addrs = append(oas.addrs, oa)

	return false
}

func (oas *dialedAddrsManager) gc() {
	now := time.Now()
	filteredAddrs := make([]*observedAddr, 0, len(oas.addrs))
	for i := range oas.addrs {
		a := oas.addrs[i]
		// clean up SeenBy set
		for k, ob := range a.seenBy {
			if now.Sub(ob.seenTime) > oas.ttl*time.Duration(activationThresh) {
				delete(a.seenBy, k)
			}
		}

		// leave only alive observed addresses
		if now.Sub(a.lastSeen) <= oas.ttl {
			filteredAddrs = append(filteredAddrs, a)
		}
	}

	oas.addrs = filteredAddrs
}

// observerGroup is a function that determines what part of
// a multiaddr counts as a different observer. for example,
// two ipfs nodes at the same IP/TCP transport would get
// the exact same NAT mapping; they would count as the
// same observer. This may protect against NATs who assign
// different ports to addresses at different IP hosts, but
// not TCP ports.
//
// Here, we use the root multiaddr address. This is mostly
// IP addresses. In practice, this is what we want.
func observerGroup(m ma.Multiaddr) string {
	//TODO: If IPv6 rolls out we should mark /64 routing zones as one group
	first, _ := ma.SplitFirst(m)
	return string(first.Bytes())
}

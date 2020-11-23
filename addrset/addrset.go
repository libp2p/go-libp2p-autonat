package addrset

import (
	ma "github.com/multiformats/go-multiaddr"
)

// AddrState describes the state of a dialable addresses as seen by AutoNAT.
type AddrState int

const (
	// StateUnknown is used for new entries.
	StateUnknown AddrState = iota
	// UnconfirmedListenUnknown applies to interface listen addresses that have unknown reachability.
	UnconfirmedListenUnknown

	//  UnconfirmedExternalUnknown applies to external observed addresses with unknown reachability.
	UnconfirmedExternalUnknown

	// Private is applied to addresses that have been deemed to have private reachability by AutoNAT.
	Private

	// Confirmed is applied to peers that have been confirmed to be dialable by AutoNAT.
	Confirmed
)

// DialAddrsSet maintains the state of a dialable addresses as seen by AutoNAT.
// The state is a set of  multi-addresses, each labeled with a AddrState.
type DialAddrsSet struct {
	// all known addresses
	all []dialAddrState
}

type dialAddrState struct {
	addr  ma.Multiaddr
	state AddrState
}

// DialAddrsSet creates a new empty set of dial addresses.
func NewDialAddrsSet() *DialAddrsSet {
	return &DialAddrsSet{
		all: []dialAddrState{},
	}
}

func (ds *DialAddrsSet) find(a ma.Multiaddr) int {
	for i := range ds.all {
		if ds.all[i].addr.Equal(a) {
			return i
		}
	}
	return -1
}

// TryAdd adds the address a to the address set.
// If the address is already present, no action is taken.
// Otherwise, the address is added with state set to StateUnknown.
// TryAdd returns true if the address was not already present.
func (ds *DialAddrsSet) TryAdd(a ma.Multiaddr) bool {
	if ds.find(a) >= 0 {
		return false
	} else {
		ds.all = append(ds.all,
			dialAddrState{addr: a, state: StateUnknown})
		return true
	}
}

// SetState sets the state of address a to s.
// If a  is not in the address set, SetState panics.
func (ds *DialAddrsSet) SetState(a ma.Multiaddr, s AddrState) {
	ds.all[ds.find(a)].state = s
}

// GetState returns the state of address a.
// If a is not in the address set, GetState panics.
func (ds *DialAddrsSet) GetState(a ma.Multiaddr) AddrState {
	return ds.all[ds.find(a)].state
}

// GetAddrsInState returns multiaddrs which are in one of the given states
func (ds *DialAddrsSet) GetAddrsInStates(states ...AddrState) []ma.Multiaddr {
	var addrs []ma.Multiaddr
	for _, a := range ds.all {
		for _, s := range states {
			if a.state == s {
				addrs = append(addrs, a.addr)
			}
		}
	}

	return addrs
}

// AssertStateAndRemove aserts that address a is in the given state s (panics if is NOT).
// It then remove the address from the address set.
// AssertStateAndRemove will panic if a is NOT in the address set.
func (ds *DialAddrsSet) AssertStateAndRemove(a ma.Multiaddr, s AddrState) {
	index := ds.find(a)

	if ds.all[index].state != s {
		panic("unexpected state")
	}

	ds.all[index] = ds.all[len(ds.all)-1]
	ds.all = ds.all[:len(ds.all)-1]
}

package autonat

import (
	"context"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// AutoNAT is the interface for NAT autodiscovery
type AutoNAT interface {
	// Status returns the current NAT status
	Status() network.Reachability
	// PublicDialAddrs returns the public dial addresses as determined by AutoNAT.
	PublicDialAddrs() ([]ma.Multiaddr, error)
}

type DialBackResult struct {
	results map[ma.Multiaddr]network.Reachability
}

// Client is a stateless client interface to AutoNAT peers
type Client interface {
	// DialBack requests from a peer providing AutoNAT services to test dial back
	// and reports the addresses that were successfully dialled and the addresses for which dialbacks failed.
	DialBack(ctx context.Context, p peer.ID, addrsToDial []ma.Multiaddr) (success, failed []ma.Multiaddr, observer ma.Multiaddr, err error)
}

// AddrFunc is a function returning the candidate addresses for the local host.
type AddrFunc func() []ma.Multiaddr

// Option is an Autonat option for configuration
type Option func(*config) error

package autonat

import (
	"github.com/libp2p/go-libp2p-core/host"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

type dialPolicy struct {
	allowSelfDials bool
	host           host.Host
}

// skipDial indicates that a multiaddress isn't worth attempted dialing.
// The same logic is used when the autonat client is considering if
// a remote peer is worth using as a server, and when the server is
// considering if a requested client is worth dialing back.
func (d *dialPolicy) skipDial(addr ma.Multiaddr) bool {
	// skip relay addresses
	_, err := addr.ValueForProtocol(ma.P_CIRCUIT)
	if err == nil {
		return true
	}

	if d.allowSelfDials {
		return false
	}

	// skip private network (unroutable) addresses
	if !manet.IsPublicAddr(addr) {
		return true
	}
	candidateIP, err := manet.ToIP(addr)
	if err != nil {
		return true
	}

	// Skip dialing addresses we believe are the local node's
	for _, localAddr := range d.host.Addrs() {
		localIP, err := manet.ToIP(localAddr)
		if err != nil {
			continue
		}
		if localIP.Equal(candidateIP) {
			return true
		}
	}

	return false
}

// skipPeer indicates that the collection of multiaddresses representing a peer
// isn't worth attempted dialing. Addresses are dialed individually, and while
// individual addresses for a peer may be worth considering, there are some
// factors, like the presence of the same public address as the local host,
// that may make the peer undesirable to dial as a whole.
func (d *dialPolicy) skipPeer(addrs []ma.Multiaddr) bool {
	goodAddr := false
	for _, addr := range addrs {
		if !d.skipDial(addr) {
			goodAddr = true
			// if a public IP of the peer is one of ours: skip the peer.
			aIP, err := manet.ToIP(addr)
			if err != nil {
				continue
			}
			aHost, err := manet.FromIP(aIP)
			if err != nil {
				continue
			}
			// its public, but it matches on of our local addresses.
			// disqualify the peer.
			if len(manet.AddrMatch(aHost, d.host.Addrs())) > 0 {
				return true
			}
		}
	}
	return !goodAddr
}

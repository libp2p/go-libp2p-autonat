package autonat

import (
	"time"

	"github.com/libp2p/go-libp2p-core/network"

	ma "github.com/multiformats/go-multiaddr"
)

var _ network.Notifiee = (*AmbientAutoNAT)(nil)

var AutoNATIdentifyDelay = 5 * time.Second

func (as *AmbientAutoNAT) Listen(net network.Network, a ma.Multiaddr)         {}
func (as *AmbientAutoNAT) ListenClose(net network.Network, a ma.Multiaddr)    {}
func (as *AmbientAutoNAT) OpenedStream(net network.Network, s network.Stream) {}
func (as *AmbientAutoNAT) ClosedStream(net network.Network, s network.Stream) {}

func (as *AmbientAutoNAT) Connected(net network.Network, c network.Conn) {
	select {
	case as.candidatePeers <- c:
	default:
	}
}

func (as *AmbientAutoNAT) Disconnected(net network.Network, c network.Conn) {}

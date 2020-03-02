package autonat

import (
	"github.com/libp2p/go-libp2p-core/network"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/patrickmn/go-cache"
	"time"
)

var _ network.Notifiee = (*dialBackTracker)(nil)

type dialBackTracker client

func (d *dialBackTracker) Listen(net network.Network, a ma.Multiaddr)         {}
func (d *dialBackTracker) ListenClose(net network.Network, a ma.Multiaddr)    {}
func (d *dialBackTracker) OpenedStream(net network.Network, s network.Stream) {}
func (d *dialBackTracker) ClosedStream(net network.Network, s network.Stream) {}

func (d *dialBackTracker) Connected(net network.Network, c network.Conn) {
	dialer := c.RemotePeer()

	if c.Stat().Direction == network.DirInbound {
		d.inboundDials.Set(dialer.String(), time.Now(), cache.DefaultExpiration)
	}
}

func (as *dialBackTracker) Disconnected(net network.Network, c network.Conn) {}

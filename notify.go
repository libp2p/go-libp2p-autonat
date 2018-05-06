package autonat

import (
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
	ma "github.com/multiformats/go-multiaddr"
)

var _ inet.Notifiee = (*AutoNATState)(nil)

func (as *AutoNATState) Listen(net inet.Network, a ma.Multiaddr)      {}
func (as *AutoNATState) ListenClose(net inet.Network, a ma.Multiaddr) {}
func (as *AutoNATState) OpenedStream(net inet.Network, s inet.Stream) {}
func (as *AutoNATState) ClosedStream(net inet.Network, s inet.Stream) {}

func (as *AutoNATState) Connected(net inet.Network, c inet.Conn) {
	go func(p peer.ID) {
		s, err := as.host.NewStream(as.ctx, p, AutoNATProto)
		if err != nil {
			return
		}
		s.Close()

		log.Infof("Discovered AutoNAT peer %s", p.Pretty())
		as.mx.Lock()
		as.peers[p] = struct{}{}
		as.mx.Unlock()
	}(c.RemotePeer())
}

func (as *AutoNATState) Disconnected(net inet.Network, c inet.Conn) {}

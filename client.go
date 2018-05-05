package autonat

import (
	"context"
	"fmt"

	pb "github.com/libp2p/go-libp2p-autonat/pb"

	ggio "github.com/gogo/protobuf/io"
	host "github.com/libp2p/go-libp2p-host"
	inet "github.com/libp2p/go-libp2p-net"
	peer "github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

type AutoNATClient interface {
	Dial(ctx context.Context) (ma.Multiaddr, error)
}

type AutoNATError struct {
	Status pb.Message_ResponseStatus
	Text   string
}

func NewAutoNATClient(h host.Host, p peer.ID) AutoNATClient {
	return &client{h: h, p: p}
}

type client struct {
	h host.Host
	p peer.ID
}

func (c *client) Dial(ctx context.Context) (ma.Multiaddr, error) {
	s, err := c.h.NewStream(ctx, c.p, AutoNATProto)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	r := ggio.NewDelimitedReader(s, inet.MessageSizeMax)
	w := ggio.NewDelimitedWriter(s)

	req := newDialMessage(pstore.PeerInfo{ID: c.h.ID(), Addrs: c.h.Addrs()})
	err = w.WriteMsg(req)
	if err != nil {
		return nil, err
	}

	var res pb.Message
	err = r.ReadMsg(&res)
	if err != nil {
		return nil, err
	}

	if res.GetType() != pb.Message_DIAL_RESPONSE {
		return nil, fmt.Errorf("Unexpected response: %s", res.GetType().String())
	}

	status := res.GetDialResponse().GetStatus()
	switch status {
	case pb.Message_OK:
		addr := res.GetDialResponse().GetAddr()
		return ma.NewMultiaddrBytes(addr)

	default:
		return nil, AutoNATError{Status: status, Text: res.GetDialResponse().GetStatusText()}
	}
}

func (e AutoNATError) Error() string {
	return fmt.Sprintf("AutoNAT error: %s (%s)", e.Text, e.Status.String())
}

func (e AutoNATError) IsDialError() bool {
	return e.Status == pb.Message_E_DIAL_ERROR
}

func IsDialError(e error) bool {
	ae, ok := e.(AutoNATError)
	return ok && ae.IsDialError()
}

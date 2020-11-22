package autonat

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	pb "github.com/libp2p/go-libp2p-autonat/pb"

	protoio "github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"
)

// Error wraps errors signalled by AutoNAT services
type Error struct {
	Status pb.Message_ResponseStatus
	Text   string
}

// NewAutoNATClient creates a fresh instance of an AutoNATClient
// If addrFunc is nil, h.Addrs will be used
func NewAutoNATClient(h host.Host) Client {
	return &client{h: h}
}

type client struct {
	h host.Host
}

func (c *client) DialBack(ctx context.Context, p peer.ID, addrsToDial []ma.Multiaddr) (success, failed []ma.Multiaddr, observer ma.Multiaddr, err error) {
	s, err := c.h.NewStream(ctx, p, AutoNATProto)
	if err != nil {
		return nil, nil, nil, err
	}
	// Might as well just reset the stream. Once we get to this point, we
	// don't care about being nice.
	defer s.Close()

	r := protoio.NewDelimitedReader(s, network.MessageSizeMax)
	w := protoio.NewDelimitedWriter(s)

	req := newDialMessage(peer.AddrInfo{ID: c.h.ID(), Addrs: addrsToDial})
	err = w.WriteMsg(req)
	if err != nil {
		s.Reset()
		return nil, nil, nil, err
	}

	var res pb.Message
	err = r.ReadMsg(&res)
	if err != nil {
		s.Reset()
		return nil, nil, nil, err
	}

	if res.GetType() != pb.Message_DIAL_RESPONSE {
		return nil, nil, nil, fmt.Errorf("Unexpected response: %s", res.GetType().String())
	}

	status := res.GetDialResponse().GetStatus()
	switch status {
	case pb.Message_OK:
		res := res.GetDialResponse()
		success = make([]ma.Multiaddr, 0, len(res.SuccessAddrs))
		for _, ab := range res.SuccessAddrs {
			if a, err := ma.NewMultiaddrBytes(ab); err == nil {
				success = append(success, a)
			}
		}

		failed = make([]ma.Multiaddr, 0, len(res.FailedAddrs))
		for _, ab := range res.FailedAddrs {
			if a, err := ma.NewMultiaddrBytes(ab); err == nil {
				failed = append(failed, a)
			}
		}
		return success, failed, s.Conn().RemoteMultiaddr(), nil

	default:
		return nil, nil, nil, Error{Status: status, Text: res.GetDialResponse().GetStatusText()}
	}
}

// 1. Aim is to diversify the server's dial attempts across address groups where the grouping key is (IP address + transport protocol(port dosen't matter))
// 2. We will continue dialing addresses in a group till we see a successful dial for an address.
// 3. If we see a successful address, we move on to the next group.
// 4. If we never see a success, we will exhaust all the addresses in a group and not visit it again.
// 5. Keep cycling through the groups till we either exhaust all addresses or see enough successful dials.
func rankedAddresses(addrs []ma.Multiaddr) []ma.Multiaddr {
	return addrs
}

func (e Error) Error() string {
	return fmt.Sprintf("AutoNAT error: %s (%s)", e.Text, e.Status.String())
}

// IsDialError returns true if the error was due to a dial back failure
func (e Error) IsDialError() bool {
	return e.Status == pb.Message_E_DIAL_ERROR
}

// IsDialRefused returns true if the error was due to a refusal to dial back
func (e Error) IsDialRefused() bool {
	return e.Status == pb.Message_E_DIAL_REFUSED
}

// IsDialError returns true if the AutoNAT peer signalled an error dialing back
func IsDialError(e error) bool {
	ae, ok := e.(Error)
	return ok && ae.IsDialError()
}

// IsDialRefused returns true if the AutoNAT peer signalled refusal to dial back
func IsDialRefused(e error) bool {
	ae, ok := e.(Error)
	return ok && ae.IsDialRefused()
}

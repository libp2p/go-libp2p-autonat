package autonat

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/helpers"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	ggio "github.com/gogo/protobuf/io"
	pb "github.com/libp2p/go-libp2p-autonat/pb"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-varint"
	"github.com/patrickmn/go-cache"
)

// AutoNATClient is a stateless client interface to AutoNAT peers
type AutoNATClient interface {
	// DialBack requests from a peer providing AutoNAT services to test dial back
	// and report the address on a successful connection.
	DialBack(ctx context.Context, p peer.ID) (ma.Multiaddr, error)
}

// AutoNATError is the class of errors signalled by AutoNAT services
type AutoNATError struct {
	Status pb.Message_ResponseStatus
	Text   string
}

// GetAddrs is a function that returns the addresses to dial back
type GetAddrs func() []ma.Multiaddr

// NewAutoNATClient creates a fresh instance of an AutoNATClient
// If getAddrs is nil, h.Addrs will be used
func NewAutoNATClient(h host.Host, getAddrs GetAddrs) AutoNATClient {
	if getAddrs == nil {
		getAddrs = h.Addrs
	}

	c := &client{
		h:            h,
		getAddrs:     getAddrs,
		inboundDials: cache.New(10*time.Minute, 10*time.Minute),
	}

	// listen to network notifications to track inbound dials
	h.Network().Notify((*dialBackTracker)(c))

	return c
}

type client struct {
	h        host.Host
	getAddrs GetAddrs

	// this is thread-safe
	inboundDials *cache.Cache
}

func (c *client) DialBack(ctx context.Context, p peer.ID) (ma.Multiaddr, error) {
	s, err := c.h.NewStream(ctx, p, AutoNATProto)
	if err != nil {
		return nil, err
	}
	// Might as well just reset the stream. Once we get to this point, we
	// don't care about being nice.
	defer helpers.FullClose(s)

	r := ggio.NewDelimitedReader(s, network.MessageSizeMax)
	w := ggio.NewDelimitedWriter(s)

	nonce := rand.Uint64()
	req := newDialMessage(peer.AddrInfo{ID: c.h.ID(), Addrs: c.getAddrs()}, nonce)
	reqTime := time.Now()
	err = w.WriteMsg(req)
	if err != nil {
		s.Reset()
		return nil, err
	}

	var res pb.Message
	err = r.ReadMsg(&res)
	if err != nil {
		s.Reset()
		return nil, err
	}

	if res.GetType() != pb.Message_DIAL_RESPONSE {
		return nil, fmt.Errorf("Unexpected response: %s", res.GetType().String())
	}

	// validate dialer identity certificate
	dialerId, err := dialerIdFromCertificate(res.DialResponse, nonce)
	if err != nil {
		return nil, fmt.Errorf("failed to extract dialerId from the Identity certificate, err=%s", err)
	}
	if dialerId == p {
		return nil, errors.New("autoNat server & it's dialer should not have the same identity")
	}

	// validate that we did indeed get a connection from this Id
	connTime, ok := c.inboundDials.Get(dialerId.String())
	if !ok {
		return nil, errors.New("no known inbound dial from the AutoNat server's dialer")
	}

	if !connTime.(time.Time).After(reqTime) {
		return nil, errors.New("autoNat server didn't dial between now & request time")
	}
	c.inboundDials.Delete(dialerId.String())

	status := res.GetDialResponse().GetStatus()
	switch status {
	case pb.Message_OK:
		addr := res.GetDialResponse().GetAddr()
		return ma.NewMultiaddrBytes(addr)

	default:
		return nil, AutoNATError{Status: status, Text: res.GetDialResponse().GetStatusText()}
	}
}

func dialerIdFromCertificate(res *pb.Message_DialResponse, nonce uint64) (peer.ID, error) {
	dc := res.DialerIdentityCertificate
	if dc == nil {
		return "", errors.New("server did not return an identity certificate for the dialer")
	}
	pk, err := crypto.PublicKeyFromProto(dc.PublicKey)
	if err != nil {
		return "", fmt.Errorf("dialer public key in identity certificate is not valid, err=%s", err)
	}

	valid, err := pk.Verify(varint.ToUvarint(nonce), dc.Signature)
	if err != nil {
		return "", fmt.Errorf("failed to verify signature on the dialer identity certificate, err=%s", err)
	}
	if !valid {
		return "", errors.New("invalid signature on the dialer identity certificate")
	}

	// extract the peerId of the dialer
	dialerId, err := peer.IDFromPublicKey(pk)
	if err != nil {
		return "", fmt.Errorf("failed to convert the dialer public key to peerId,err=%s", err)
	}
	return dialerId, nil
}

func (e AutoNATError) Error() string {
	return fmt.Sprintf("AutoNAT error: %s (%s)", e.Text, e.Status.String())
}

func (e AutoNATError) IsDialError() bool {
	return e.Status == pb.Message_E_DIAL_ERROR
}

func (e AutoNATError) IsDialRefused() bool {
	return e.Status == pb.Message_E_DIAL_REFUSED
}

// IsDialError returns true if the AutoNAT peer signalled an error dialing back
func IsDialError(e error) bool {
	ae, ok := e.(AutoNATError)
	return ok && ae.IsDialError()
}

// IsDialRefused returns true if the AutoNAT peer signalled refusal to dial back
func IsDialRefused(e error) bool {
	ae, ok := e.(AutoNATError)
	return ok && ae.IsDialRefused()
}

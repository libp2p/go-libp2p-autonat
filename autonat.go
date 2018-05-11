package autonat

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	host "github.com/libp2p/go-libp2p-host"
	peer "github.com/libp2p/go-libp2p-peer"
	ma "github.com/multiformats/go-multiaddr"
)

type NATStatus int

const (
	NATStatusUnknown NATStatus = iota
	NATStatusPublic
	NATStatusPrivate
)

var (
	AutoNATBootDelay       = 15 * time.Second
	AutoNATRefreshInterval = 15 * time.Minute

	AutoNATRequestTimeout = 60 * time.Second
)

type AutoNAT interface {
	Status() NATStatus
	PublicAddr() (ma.Multiaddr, error)
}

type AmbientAutoNAT struct {
	ctx  context.Context
	host host.Host

	mx     sync.Mutex
	peers  map[peer.ID]struct{}
	status NATStatus
	addr   ma.Multiaddr
}

func NewAutoNAT(ctx context.Context, h host.Host) AutoNAT {
	as := &AmbientAutoNAT{
		ctx:    ctx,
		host:   h,
		peers:  make(map[peer.ID]struct{}),
		status: NATStatusUnknown,
	}

	h.Network().Notify(as)
	go as.background()

	return as
}

func (as *AmbientAutoNAT) Status() NATStatus {
	return as.status
}

func (as *AmbientAutoNAT) PublicAddr() (ma.Multiaddr, error) {
	as.mx.Lock()
	defer as.mx.Unlock()

	if as.status != NATStatusPublic {
		return nil, errors.New("NAT Status is not public")
	}

	return as.addr, nil
}

func (as *AmbientAutoNAT) background() {
	// wait a bit for the node to come online and establish some connections
	// before starting autodetection
	time.Sleep(AutoNATBootDelay)
	for {
		as.autodetect()
		select {
		case <-time.After(AutoNATRefreshInterval):
		case <-as.ctx.Done():
			return
		}
	}
}

func (as *AmbientAutoNAT) autodetect() {
	peers := as.getPeers()

	if len(peers) == 0 {
		log.Debugf("skipping NAT auto detection; no autonat peers")
		return
	}

	cli := NewAutoNATClient(as.host)

	for _, p := range peers {
		ctx, cancel := context.WithTimeout(as.ctx, AutoNATRequestTimeout)
		a, err := cli.Dial(ctx, p)
		cancel()

		switch {
		case err == nil:
			log.Debugf("NAT status is public; address through %s: %s", p.Pretty(), a.String())
			as.mx.Lock()
			as.addr = a
			as.status = NATStatusPublic
			as.mx.Unlock()
			return

		case IsDialError(err):
			log.Debugf("NAT status is private; dial error through %s: %s", p.Pretty(), err.Error())
			as.mx.Lock()
			as.status = NATStatusPrivate
			as.mx.Unlock()
			return

		default:
			log.Debugf("Error dialing through %s: %s", p.Pretty(), err.Error())
		}
	}

	as.mx.Lock()
	as.status = NATStatusUnknown
	as.mx.Unlock()
}

func (as *AmbientAutoNAT) getPeers() []peer.ID {
	as.mx.Lock()
	defer as.mx.Unlock()

	if len(as.peers) == 0 {
		return nil
	}

	peers := make([]peer.ID, 0, len(as.peers))
	for p := range as.peers {
		if len(as.host.Network().ConnsToPeer(p)) > 0 {
			peers = append(peers, p)
		}
	}

	if len(peers) == 0 {
		// we don't have any open connections, try any autonat peer that we know about
		for p := range as.peers {
			peers = append(peers, p)
		}
	}

	shufflePeers(peers)

	return peers
}

func shufflePeers(peers []peer.ID) {
	for i := range peers {
		j := rand.Intn(i + 1)
		peers[i], peers[j] = peers[j], peers[i]
	}
}

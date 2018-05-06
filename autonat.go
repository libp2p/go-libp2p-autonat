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

// attach to any host and auto-discovery autonat servers
// periodically query them to deal with changing NAT situation

type NATStatus int

const (
	NATStatusUnknown = iota
	NATStatusPublic
	NATStatusPrivate
)

type AutoNAT interface {
	Status() NATStatus
	PublicAddr() (ma.Multiaddr, error)
}

type AutoNATState struct {
	ctx    context.Context
	host   host.Host
	peers  map[peer.ID]bool
	status NATStatus
	addr   ma.Multiaddr
	mx     sync.Mutex
}

func NewAutoNAT(ctx context.Context, h host.Host) AutoNAT {
	as := &AutoNATState{
		ctx:    ctx,
		host:   h,
		peers:  make(map[peer.ID]bool),
		status: NATStatusUnknown,
	}

	go as.background()
	return as
}

func (as *AutoNATState) Status() NATStatus {
	return as.status
}

func (as *AutoNATState) PublicAddr() (ma.Multiaddr, error) {
	as.mx.Lock()
	defer as.mx.Unlock()

	if as.status != NATStatusPublic {
		return nil, errors.New("NAT Status is not public")
	}

	return as.addr, nil
}

func (as *AutoNATState) background() {
	// wait a bit for the node to come online and establish some connections
	// before starting autodetection
	time.Sleep(10 * time.Second)
	for {
		as.autodetect()
		select {
		case <-time.After(15 * time.Minute):
		case <-as.ctx.Done():
			return
		}
	}
}

func (as *AutoNATState) autodetect() {
	if len(as.peers) == 0 {
		log.Debugf("skipping NAT auto detection; no autonat peers")
		return
	}

	as.mx.Lock()
	peers := make([]peer.ID, 0, len(as.peers))
	for p, c := range as.peers {
		if c {
			peers = append(peers, p)
		}
	}

	if len(peers) == 0 {
		// we don't have any open connections, try any autonat peer that we know about
		for p := range as.peers {
			peers = append(peers, p)
		}
	}

	as.mx.Unlock()

	shufflePeers(peers)

	for _, p := range peers {
		cli := NewAutoNATClient(as.host, p)
		ctx, cancel := context.WithTimeout(as.ctx, 60*time.Second)
		a, err := cli.Dial(ctx)
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

func shufflePeers(peers []peer.ID) {
	for i := range peers {
		j := rand.Intn(i + 1)
		peers[i], peers[j] = peers[j], peers[i]
	}
}

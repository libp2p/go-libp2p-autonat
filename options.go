package autonat

import (
	"errors"
	"time"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

// config holds configurable options for the autonat subsystem.
type config struct {
	host host.Host

	dialPolicy        dialPolicy
	transports        []transport.Transport
	forceReachability bool
	reachability      network.Reachability
	dialAddrs         []ma.Multiaddr

	// client
	bootDelay          time.Duration
	retryInterval      time.Duration
	refreshInterval    time.Duration
	requestTimeout     time.Duration
	throttlePeerPeriod time.Duration

	// server
	dialTimeout         time.Duration
	maxPeerAddresses    int
	maxSuccessfulDials  int
	throttleGlobalMax   int
	throttlePeerMax     int
	throttleResetPeriod time.Duration
	throttleResetJitter time.Duration
}

var defaults = func(c *config) error {
	c.bootDelay = 15 * time.Second
	c.retryInterval = 90 * time.Second
	c.refreshInterval = 15 * time.Minute
	c.requestTimeout = 30 * time.Second
	c.throttlePeerPeriod = 90 * time.Second

	c.dialTimeout = 15 * time.Second
	c.maxPeerAddresses = 16
	c.maxSuccessfulDials = 4
	c.throttleGlobalMax = 30
	c.throttlePeerMax = 3
	c.throttleResetPeriod = 1 * time.Minute
	c.throttleResetJitter = 15 * time.Second
	return nil
}

// EnableService specifies that AutoNAT should be allowed to run a NAT service to help
// other peers determine their own NAT status. The resulting NAT service
// will dial back addresses supported by one of the given transports.
func EnableService(transports ...transport.Transport) Option {
	return func(c *config) error {
		if len(transports) == 0 {
			return errors.New("no transports given")
		}
		c.transports = append(c.transports, transports...)
		return nil
	}
}

// WithReachability overrides autonat to simply report an over-ridden reachability
// status.
func WithReachability(reachability network.Reachability) Option {
	return func(c *config) error {
		c.forceReachability = true
		c.reachability = reachability
		return nil
	}
}

// WithSchedule configures how agressively probes will be made to verify the
// address of the host. retryInterval indicates how often probes should be made
// when the host lacks confident about its address, while refresh interval
// is the schedule of periodic probes when the host believes it knows its
// steady-state reachability.
func WithSchedule(retryInterval, refreshInterval time.Duration) Option {
	return func(c *config) error {
		c.retryInterval = retryInterval
		c.refreshInterval = refreshInterval
		return nil
	}
}

// WithoutStartupDelay removes the initial delay the NAT subsystem typically
// uses as a buffer for ensuring that connectivity and guesses as to the hosts
// local interfaces have settled down during startup.
func WithoutStartupDelay() Option {
	return func(c *config) error {
		c.bootDelay = 1
		return nil
	}
}

// WithoutThrottling indicates that this autonat service should not place
// restrictions on how many peers it is willing to help when acting as
// a server.
func WithoutThrottling() Option {
	return func(c *config) error {
		c.throttleGlobalMax = 0
		return nil
	}
}

// WithThrottling specifies how many peers (`amount`) it is willing to help
// ever `interval` amount of time when acting as a server.
func WithThrottling(amount int, interval time.Duration) Option {
	return func(c *config) error {
		c.throttleGlobalMax = amount
		c.throttleResetPeriod = interval
		c.throttleResetJitter = interval / 4
		return nil
	}
}

// WithPeerThrottling specifies a limit for the maximum number of IP checks
// this node will provide to an individual peer in each `interval`.
func WithPeerThrottling(amount int) Option {
	return func(c *config) error {
		c.throttlePeerMax = amount
		return nil
	}
}

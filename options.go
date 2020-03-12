package autonat

import (
	"time"
)

// config holds configurable options for the autonat subsystem.
type config struct {
	getAddressFunc  GetAddrs
	bootDelay       time.Duration
	retryInterval   time.Duration
	refreshInterval time.Duration
	requestTimeout  time.Duration
}

var defaults = func(c *config) error {
	c.bootDelay = 15 * time.Second
	c.retryInterval = 90 * time.Second
	c.refreshInterval = 15 * time.Minute
	c.requestTimeout = 30 * time.Second

	return nil
}

// WithAddresses allows overriding which Addresses the AutoNAT client beliieves
// are "its own". Useful for testing, or for more exotic port-forwarding
// scenarios where the host may be listening on different ports than it wants
// to externally advertise or verify connectability on.
func WithAddresses(addrFunc GetAddrs) Option {
	return func(c *config) error {
		c.getAddressFunc = addrFunc
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

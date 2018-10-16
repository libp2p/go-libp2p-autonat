package autonat

import (
	"net"

	ma "github.com/multiformats/go-multiaddr"
)

var Private4, Private6 []*net.IPNet
var privateCIDR4 = []string{
	// localhost
	"127.0.0.0/8",
	// private networks
	"10.0.0.0/8",
	"100.64.0.0/10",
	"172.16.0.0/12",
	"192.168.0.0/16",
	// link local
	"169.254.0.0/16",
}
var privateCIDR6 = []string{
	// localhost
	"::1/128",
	// ULA reserved
	"fc00::/7",
	// link local
	"fe80::/10",
}

func init() {
	Private4 = parsePrivateCIDR(privateCIDR4)
	Private6 = parsePrivateCIDR(privateCIDR6)
}

func parsePrivateCIDR(cidrs []string) []*net.IPNet {
	ipnets := make([]*net.IPNet, len(cidrs))
	for i, cidr := range cidrs {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		ipnets[i] = ipnet
	}
	return ipnets
}

func IsPublicAddr(a ma.Multiaddr) bool {
	ip, err := a.ValueForProtocol(ma.P_IP4)
	if err == nil {
		return !inAddrRange(ip, Private4)
	}

	ip, err = a.ValueForProtocol(ma.P_IP6)
	if err == nil {
		return !inAddrRange(ip, Private6)
	}

	return false
}

func inAddrRange(s string, ipnets []*net.IPNet) bool {
	ip := net.ParseIP(s)
	for _, ipnet := range ipnets {
		if ipnet.Contains(ip) {
			return true
		}
	}

	return false
}

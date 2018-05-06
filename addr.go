package autonat

import (
	"net"

	ma "github.com/multiformats/go-multiaddr"
)

var private4, private6 []*net.IPNet
var privateCIDR4 = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"169.254.0.0/16",
}
var privateCIDR6 = []string{
	"fc00::/7",
	"fe80::/10",
}

func init() {
	private4 = parsePrivateCIDR(privateCIDR4)
	private6 = parsePrivateCIDR(privateCIDR6)
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

func isPublicAddr(a ma.Multiaddr) bool {
	ip, err := a.ValueForProtocol(ma.P_IP4)
	if err == nil {
		return !inAddrRange(ip, private4)
	}

	ip, err = a.ValueForProtocol(ma.P_IP6)
	if err == nil {
		return !inAddrRange(ip, private6)
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

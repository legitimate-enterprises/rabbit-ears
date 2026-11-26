package playback

import "net"

// SameLAN is the local rule: the client's address is in one of the server's
// interface subnets and the client found the server by local discovery. A
// VPN into the house is therefore remote, since bandwidth is the question.
func SameLAN(addr net.IP, subnets []*net.IPNet, discovered bool) bool {
	if !discovered || addr == nil {
		return false
	}
	for _, n := range subnets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// LocalAddresses picks the private IPv4 addresses among the server's own:
// where a device on its network can reach it.
func LocalAddresses(subnets []*net.IPNet) []net.IP {
	var out []net.IP
	for _, n := range subnets {
		if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
			out = append(out, ip)
		}
	}
	return out
}

// InterfaceSubnets lists the subnets of the server's own interfaces.
func InterfaceSubnets() []*net.IPNet {
	addrs, _ := net.InterfaceAddrs()
	var out []*net.IPNet
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n)
		}
	}
	return out
}

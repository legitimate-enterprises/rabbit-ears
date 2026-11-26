package playback_test

import (
	"net"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
)

func TestLocalAddressesArePrivateIPv4(t *testing.T) {
	var subnets []*net.IPNet
	for _, c := range []string{"127.0.0.1/8", "10.0.0.2/24", "100.64.0.1/32", "192.168.0.3/23", "203.0.113.9/24", "fd00::1/64"} {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		subnets = append(subnets, n)
	}
	got := playback.LocalAddresses(subnets)
	if len(got) != 2 || got[0].String() != "10.0.0.2" || got[1].String() != "192.168.0.3" {
		t.Fatalf("got %v", got)
	}
}

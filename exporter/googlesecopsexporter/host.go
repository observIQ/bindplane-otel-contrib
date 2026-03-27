package googlesecopsexporter

import (
	"net"
	"strings"
)

const unknownValue = "unknown"

// macAddress returns the MAC address of the first network interface
// that has a valid, non-loopback IPv4 address.
func macAddress() string {
	return findMACAddress(net.Interfaces)
}

// findMACAddress iterates over network interfaces and returns the hardware
// address of the first interface with a valid IPv4 address.
func findMACAddress(interfaces func() ([]net.Interface, error)) string {
	ifaces, err := interfaces()
	if err != nil {
		return unknownValue
	}

	for _, iface := range ifaces {
		if iface.HardwareAddr.String() == "" {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			address := addr.String()
			if strings.Contains(address, "/") {
				address = address[:strings.Index(address, "/")]
			}
			ip := net.ParseIP(address)
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				return iface.HardwareAddr.String()
			}
		}
	}

	return unknownValue
}

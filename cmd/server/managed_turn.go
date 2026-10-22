package main

import (
	"net"
	"strconv"

	"noxa/internal/config"
)

// The co-located SFU's exact bound UDP endpoint is the only automatic private
// destination exception. Discover it on each boot so container IP changes do
// not require granting relay access to an entire database/container network.
func managedTURNConfig(cfg *config.Config) config.TURNConfig {
	out := cfg.TURN
	out.AllowedPeers = append([]string(nil), out.AllowedPeers...)
	host, port, err := net.SplitHostPort(cfg.WebRTC.UDPAddr)
	if err != nil {
		return out
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return out
	}
	ips := append([]string(nil), cfg.WebRTC.ExternalIPs...)
	if host == "" || host == "0.0.0.0" {
		if addresses, err := net.InterfaceAddrs(); err == nil {
			for _, address := range addresses {
				if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil {
					ips = append(ips, network.IP.String())
				}
			}
		}
	} else {
		ips = append(ips, host)
	}
	for _, ip := range ips {
		if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil && !parsed.IsUnspecified() {
			out.AllowedPeers = append(out.AllowedPeers, net.JoinHostPort(ip, port))
		}
	}
	return out
}

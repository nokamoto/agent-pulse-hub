package control

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

const DefaultAddress = "127.0.0.1:47831"

const operationTimeout = 25 * time.Second

func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("control address must contain a literal IPv4 loopback address and port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() || !ip.IsLoopback() {
		return errors.New("control address must use a literal IPv4 loopback address")
	}
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return errors.New("control port must be an integer from 1 through 65535")
		}
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("control port must be an integer from 1 through 65535")
	}
	return nil
}

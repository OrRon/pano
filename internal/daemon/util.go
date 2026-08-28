package daemon

import (
	"net"
	"strconv"
)

func netSplit(addr string) (string, int, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, err
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = "127.0.0.1"
	}
	return h, port, nil
}

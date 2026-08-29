package daemon

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/mobile"
	"github.com/orron/pano/internal/proxy"
)

// Mobile implements control.Backend: the LAN listener state plus every
// remote device the proxy has seen.
func (d *Daemon) Mobile(context.Context) api.Mobile {
	d.mobileMu.Lock()
	ln, lan, last := d.mobileLn, d.mobileLAN, d.mobileLast
	d.mobileMu.Unlock()
	return d.mobileStateLast(ln, lan, last)
}

// mobileState renders the state for a given listener; the caller holds
// mobileMu.
func (d *Daemon) mobileState(ln net.Listener, lan mobile.LAN) api.Mobile {
	return d.mobileStateLast(ln, lan, d.mobileLast)
}

func (d *Daemon) mobileStateLast(ln net.Listener, lan mobile.LAN, last string) api.Mobile {
	m := api.Mobile{Devices: []api.Device{}}
	for _, dev := range d.proxy.Devices() {
		m.Devices = append(m.Devices, apiDevice(dev))
	}
	if ln == nil {
		m.LastAddr = last
		return m
	}
	m.Enabled = true
	m.Addr = ln.Addr().String()
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		m.IP, m.Port = tcp.IP.String(), tcp.Port
	}
	m.Interface, m.Network = lan.Interface, lan.Network
	m.URL = "http://" + m.Addr
	m.MagicURL = "http://" + proxy.MagicHost
	if cur, err := mobile.Detect(); err == nil && cur.IP != m.IP && lan.Interface == cur.Interface {
		m.Warning = fmt.Sprintf("this Mac's address is now %s (was %s) — run `pano mobile` again", cur.IP, m.IP)
	}
	return m
}

// SetMobile implements control.Backend. Enabling binds one more proxy
// listener on the Mac's LAN address (never 0.0.0.0, never the control API or
// MCP), admitting private-network clients only. Disabling closes it; open
// tunnels finish on their own.
func (d *Daemon) SetMobile(_ context.Context, req api.MobileRequest) (api.Mobile, error) {
	d.mobileMu.Lock()
	defer d.mobileMu.Unlock()
	if !req.Enabled {
		if d.mobileLn != nil {
			_ = d.proxy.RemoveListener(d.mobileLn)
			d.mobileLast = d.mobileLn.Addr().String()
			d.log.Info("mobile: LAN listener closed", "addr", d.mobileLast)
			d.mobileLn, d.mobileLAN = nil, mobile.LAN{}
		}
		return d.mobileState(nil, mobile.LAN{}), nil
	}

	lan, err := mobile.Detect()
	if req.IP != "" {
		ip := net.ParseIP(req.IP)
		if ip == nil || ip.To4() == nil {
			return api.Mobile{}, api.BadRequest("ip must be an IPv4 address of this machine")
		}
		lan, err = mobile.LAN{IP: ip.String(), Interface: "custom"}, nil
	}
	if err != nil {
		return api.Mobile{}, err
	}
	port := req.Port
	if port == 0 {
		_, port = hostPort(d.proxy.Addr()) // the port the loopback proxy actually bound
	}
	addr := net.JoinHostPort(lan.IP, strconv.Itoa(port))
	if d.mobileLn != nil {
		if d.mobileLn.Addr().String() == addr {
			return d.mobileState(d.mobileLn, d.mobileLAN), nil
		}
		_ = d.proxy.RemoveListener(d.mobileLn)
		d.mobileLn = nil
	}
	d.mobileLast = ""
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return api.Mobile{}, fmt.Errorf("mobile: listen %s: %w", addr, err)
	}
	d.mobileLn, d.mobileLAN = mobile.PrivateOnly(ln), lan
	d.proxy.AddListener(d.mobileLn)
	d.log.Info("mobile: proxy open to the LAN", "addr", addr, "interface", lan.Interface, "network", lan.Network)
	return d.mobileState(d.mobileLn, lan), nil
}

func apiDevice(dev proxy.Device) api.Device {
	return api.Device{
		IP: dev.IP, Name: dev.Name, UserAgent: dev.UserAgent, FirstSeen: dev.FirstSeen, LastSeen: dev.LastSeen,
		Requests: dev.Requests, Decrypted: dev.Decrypted, Rejected: dev.Rejected, Proxy: dev.ProxyOK(), TLS: dev.TLSOK(),
	}
}

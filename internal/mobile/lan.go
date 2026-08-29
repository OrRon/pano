package mobile

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// LAN is the address other devices on the local network can reach this
// machine at.
type LAN struct {
	IP        string
	Interface string
	Network   string // Wi-Fi SSID when the OS will tell us; "" otherwise
}

// ErrNoLAN is returned when no interface carries a private IPv4 address.
var ErrNoLAN = errors.New("mobile: no LAN address found — is Wi-Fi (or Ethernet) connected?")

// Detect picks the interface phones are most likely to share with this
// machine: up, not loopback, not a tunnel/bridge, carrying a private IPv4
// address; Wi-Fi first (en0 on a Mac, wl* on Linux), then wired.
func Detect() (LAN, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return LAN{}, err
	}
	var cands []LAN
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || skipInterface(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			cands = append(cands, LAN{IP: ip4.String(), Interface: ifc.Name})
			break
		}
	}
	if len(cands) == 0 {
		return LAN{}, ErrNoLAN
	}
	sort.SliceStable(cands, func(i, j int) bool { return ifaceRank(cands[i].Interface) < ifaceRank(cands[j].Interface) })
	l := cands[0]
	l.Network = ssid(l.Interface)
	return l, nil
}

// Interfaces lists every usable LAN address (for `--ip` hints).
func Interfaces() []LAN {
	ifaces, _ := net.Interfaces()
	var out []LAN
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || skipInterface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
					out = append(out, LAN{IP: ip4.String(), Interface: ifc.Name})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return ifaceRank(out[i].Interface) < ifaceRank(out[j].Interface) })
	return out
}

func skipInterface(name string) bool {
	for _, p := range []string{"utun", "awdl", "llw", "bridge", "vmnet", "docker", "veth", "anpi", "ap", "gif", "stf", "tun", "tap", "ipsec", "ppp"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ifaceRank orders candidates: Wi-Fi, then wired, then the rest.
func ifaceRank(name string) int {
	switch {
	case name == "en0", strings.HasPrefix(name, "wl"):
		return 0
	case strings.HasPrefix(name, "en"), strings.HasPrefix(name, "eth"):
		return 1
	}
	return 2
}

// ssid asks macOS for the Wi-Fi network name of iface. Best effort: recent
// macOS versions hide it from some tools, and a wired interface has none.
func ssid(iface string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "ipconfig", "getsummary", iface).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(line, " SSID : "); ok && strings.TrimSpace(k) == "" {
				if v = strings.TrimSpace(v); v != "" && !strings.Contains(v, "<redacted>") {
					return v // macOS 15+ redacts it unless the caller has location access
				}
			}
		}
	}
	if out, err := exec.CommandContext(ctx, "networksetup", "-getairportnetwork", iface).Output(); err == nil {
		if _, v, ok := strings.Cut(string(out), "Current Wi-Fi Network: "); ok {
			v = strings.TrimSpace(v)
			if v != "" && !strings.Contains(v, "<redacted>") {
				return v
			}
		}
	}
	return ""
}

// PrivateOnly wraps a listener so that only clients on private or link-local
// addresses get through; anything else is closed before a byte is read. The
// LAN listener is meant for the devices next to you, not for whoever can
// route to the machine.
func PrivateOnly(ln net.Listener) net.Listener { return &privateListener{Listener: ln} }

type privateListener struct{ net.Listener }

func (p *privateListener) Accept() (net.Conn, error) {
	for {
		c, err := p.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if tcp, ok := c.RemoteAddr().(*net.TCPAddr); ok && (tcp.IP.IsPrivate() || tcp.IP.IsLinkLocalUnicast() || tcp.IP.IsLoopback()) {
			return c, nil
		}
		_ = c.Close()
	}
}

// MachineName is what the page and the profile call this computer: the
// macOS computer name ("Or's Mac mini") when available, else the hostname.
func MachineName() string {
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "scutil", "--get", "ComputerName").Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this machine"
	}
	return strings.TrimSuffix(strings.TrimSuffix(h, ".local"), ".lan")
}

// Package mobile is everything `pano mobile` needs to put a phone behind the
// proxy: finding the Mac's LAN address, a listener that only admits the local
// network, the root certificate as an Apple configuration profile and as DER
// for Android, and the setup site the phone opens — one page that names the
// proxy settings, hands out the certificate and reports, live, how far the
// device has got.
//
// The site is reachable three ways: directly at http://<lan-ip>:<port>
// before the phone proxies anything (that is what the QR code encodes),
// through the proxy at the same address once it does, and at
// http(s)://pano.internal from any client whose traffic already passes
// through pano.
package mobile

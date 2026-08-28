package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// NewTransport builds the shared upstream transport. It never consults proxy
// environment variables (which could point back at pano) and never decodes
// bodies, so captured bytes are exactly what the origin sent.
func NewTransport(tlsCfg *tls.Config) *http.Transport {
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tlsCfg = tlsCfg.Clone()
	if tlsCfg.ClientSessionCache == nil {
		tlsCfg.ClientSessionCache = tls.NewLRUClientSessionCache(2048)
	}
	if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = tls.VersionTLS12
	}
	return &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2048,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0, // LLM APIs can take minutes before the first byte
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
		WriteBufferSize:       64 << 10,
		ReadBufferSize:        64 << 10,
	}
}

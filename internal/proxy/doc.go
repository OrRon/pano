// Package proxy is pano's MITM engine: an HTTP/1.1 proxy listener that
// intercepts CONNECT tunnels, terminates TLS with certificates minted by the
// local CA, serves HTTP/1.1 or HTTP/2 on the decrypted connection, forwards to
// the origin over a pooled transport, and captures every exchange while
// streaming bodies in both directions. Rules and storage plug in through the
// Hooks and Sink interfaces so the engine has no knowledge of either.
package proxy

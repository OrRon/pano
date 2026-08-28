// Package control serves pano's local control API: HTTP/1.1 + JSON over a
// Unix socket (and optionally loopback TCP). The CLI and the MCP server are
// both thin clients of this API, which keeps their behaviour identical.
package control

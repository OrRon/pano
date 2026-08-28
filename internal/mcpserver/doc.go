// Package mcpserver exposes pano to AI agents over the Model Context Protocol.
// Every tool is a thin call into the control API client; rendering is tuned
// for token efficiency and every result ends with a "next:" hint so agents
// can escalate from list → summary → path → raw without guessing.
package mcpserver

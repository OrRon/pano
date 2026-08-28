// Package bus is a fan-out event bus with bounded per-subscriber queues. A
// slow subscriber loses events (and is told so) instead of stalling the proxy.
package bus

package store

import (
	"testing"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func TestMatchClient(t *testing.T) {
	if !MatchClient("192.168.1.40", "192.168.1.40:5000") || MatchClient("192.168.1.40", "192.168.1.41:5000") {
		t.Fatal("ip match")
	}
	if !MatchClient("remote", "192.168.1.40:5000") || MatchClient("remote", "127.0.0.1:5000") || MatchClient("remote", "[::1]:5000") || MatchClient("remote", "") {
		t.Fatal("remote match")
	}
	m := Compile(api.FlowFilter{Client: "remote"}, time.Now())
	if !m.Match(&flow.Flow{Client: "10.0.0.2:1"}) || m.Match(&flow.Flow{Client: "127.0.0.1:1"}) {
		t.Fatal("compiled match")
	}
}

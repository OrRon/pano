package sysproxy

import (
	"reflect"
	"testing"
)

func TestMergeBypass(t *testing.T) {
	got := mergeBypass(
		[]string{"*.local", "169.254/16", " corp.example ", ""},
		[]string{"localhost", "api.internal", "corp.example"},
	)
	want := []string{"*.local", "169.254/16", "corp.example", "localhost", "127.0.0.1", "api.internal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeBypass = %v, want %v", got, want)
	}
	if got := mergeBypass(nil, nil); !reflect.DeepEqual(got, DefaultBypass) {
		t.Fatalf("mergeBypass(nil, nil) = %v, want defaults %v", got, DefaultBypass)
	}
}

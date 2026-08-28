// Command origin is a tiny local HTTP origin used by bench/run.sh.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()
	small := []byte(`{"ok":true,"items":[1,2,3,4,5,6,7,8,9,10],"message":"hello from the benchmark origin server"}`)
	large := []byte(strings.Repeat("x", 1<<20))
	mux := http.NewServeMux()
	mux.HandleFunc("/small", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(small)
	})
	mux.HandleFunc("/large", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(large)
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		rc := http.NewResponseController(w)
		for i := 0; i < 20; i++ {
			_, _ = fmt.Fprintf(w, "data: {\"i\":%d}\n\n", i)
			_ = rc.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	log.Printf("origin listening on %s", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

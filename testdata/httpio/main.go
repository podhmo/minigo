package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Roundtrip runs a real HTTP server + client cycle on a loopback
// socket: the whole net/http stack below the public API is interpreted
// from GOROOT source while the unsafe/syscall leaves (net, bufio,
// sync/atomic, context, io) are bound to the host. Returns "status|body".
func Roundtrip() string {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hi %s", r.URL.Path)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "listen: " + err.Error()
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	time.Sleep(200 * time.Millisecond)
	resp, err := http.Get("http://" + ln.Addr().String() + "/hello")
	if err != nil {
		return "get: " + err.Error()
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "read: " + err.Error()
	}
	return resp.Status + "|" + string(b)
}

func main() {}

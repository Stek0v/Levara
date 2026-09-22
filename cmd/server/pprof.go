// pprof.go — opt-in runtime profiling endpoint.
//
// LEVARA_PPROF_ADDR (e.g. "127.0.0.1:6060") starts a plain net/http server
// with the standard pprof handlers: heap, goroutine, allocs, cpu, block,
// mutex, trace. Off by default — profiling is an admin-only surface and
// must not be exposed unintentionally (enterprise profiles, containers).
//
// Started immediately after flag parsing, BEFORE the heavy boot work (WAL
// replay, BM25 build, collection load), so the boot itself can be profiled
// — that is where minutes of CPU and tens of GB of heap are decided.
package main

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // registers handlers on http.DefaultServeMux
	"os"
	"strings"
)

// startPprofServer binds addr (":0" picks a free port) and serves the
// default pprof mux on it. Returns the bound address. The server runs for
// the process lifetime; a bind error is returned synchronously so a bad
// address is reported at startup instead of dying silently in a goroutine.
func startPprofServer(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	go func() {
		if err := (&http.Server{Handler: http.DefaultServeMux}).Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[pprof] server stopped: %v", err)
		}
	}()
	return ln.Addr().String(), nil
}

// pprofAddrFromEnv returns the configured profiling address, "" when off.
func pprofAddrFromEnv() string {
	return strings.TrimSpace(os.Getenv("LEVARA_PPROF_ADDR"))
}

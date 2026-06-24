package health

import (
	"context"
	"net/http"
	"time"

	"github.com/qs-lzh/flash-sale/pkg/metrics"
)

type Server struct {
	http  *http.Server
	ready int32
}

func New(addr string) *Server {
	mux := http.NewServeMux()
	s := &Server{}

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if s.ready == 1 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ready"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("not ready"))
		}
	})

	mux.Handle("/metrics", metrics.Handler())

	s.http = &http.Server{Addr: addr, Handler: mux}
	return s
}

func (s *Server) MarkReady() {
	s.ready = 1
}

func (s *Server) Start() error {
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

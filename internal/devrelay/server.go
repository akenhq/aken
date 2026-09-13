// SPDX-License-Identifier: Apache-2.0
package devrelay

import (
	"net/http"
	"time"

	"github.com/akenhq/aken/relay"
)

type Server struct {
	Now   func() time.Time
	store *relay.MemoryStore
	mux   *relay.Handler
}

func New() *Server {
	s := &Server{store: relay.NewMemoryStore()}
	s.mux = relay.NewHandler(s.store, relay.Options{Now: s.now})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Sweep() {
	s.mux.Sweep()
	s.store.Sweep(s.now())
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

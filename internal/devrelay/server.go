// SPDX-License-Identifier: Apache-2.0
package devrelay

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/akenhq/aken/protocol"
)

type Server struct {
	Now      func() time.Time
	mu       sync.Mutex
	sessions map[protocol.SessionID]*session
	mux      *http.ServeMux
}

type session struct {
	credHash   [32]byte
	expiresAt  time.Time
	chunkCount uint32
	chunks     [][]byte
	stored     uint32
	manifest   []byte
}

func New() *Server {
	s := &Server{sessions: make(map[protocol.SessionID]*session), mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v0/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, protocol.Info{ProtocolVersions: []int{1}, Caps: protocol.DefaultCaps})
	})
	for _, route := range []struct{ method, path string }{
		{"PUT", "/v0/sessions/{sid}"}, {"GET", "/v0/sessions/{sid}"}, {"DELETE", "/v0/sessions/{sid}"},
		{"PUT", "/v0/sessions/{sid}/blob/chunks/{index}"}, {"GET", "/v0/sessions/{sid}/blob/chunks/{index}"},
		{"PUT", "/v0/sessions/{sid}/blob/manifest"}, {"GET", "/v0/sessions/{sid}/blob/manifest"},
	} {
		s.mux.HandleFunc(route.method+" "+route.path, s.handleSession)
	}
	for _, path := range []string{"/v0/info", "/v0/sessions/{sid}", "/v0/sessions/{sid}/blob/chunks/{index}", "/v0/sessions/{sid}/blob/manifest"} {
		s.mux.HandleFunc(path, methodNotAllowed)
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeError(w, 404, "not_found", "not found") })
	return s
}

func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeError(w, 405, "method_not_allowed", "method not allowed")
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ServeMux routes HEAD to GET, but v0 defines only the listed methods.
	if r.Method == http.MethodHead {
		_, pattern := s.mux.Handler(r)
		if pattern != "/" {
			methodNotAllowed(w, r)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Server) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, sess := range s.sessions {
		if !now.Before(sess.expiresAt) {
			delete(s.sessions, id)
		}
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(protocol.ProtocolHeader) != "1" {
		writeJSON(w, 426, struct {
			protocol.ErrorResponse
			ProtocolVersions []int `json:"protocol_versions"`
		}{protocol.ErrorResponse{Error: "unsupported_version", Message: "protocol version not supported"}, []int{1}})
		return
	}
	id, validID := protocol.ParseSessionID(r.PathValue("sid"))
	credential, validCredential := protocol.ParseAuthorizationHeader(r.Header.Get("Authorization"))
	if !validID {
		writeError(w, 404, "not_found", "not found")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	now := s.now()
	if sess != nil && !now.Before(sess.expiresAt) {
		delete(s.sessions, id)
		sess = nil
	}
	if !validCredential {
		writeError(w, 404, "not_found", "not found")
		return
	}
	hash := sha256.Sum256(credential[:])
	isBase := r.URL.Path == "/v0/sessions/"+id.String()
	if isBase && r.Method == http.MethodPut {
		if sess != nil {
			writeError(w, 409, "already_exists", "already exists")
			return
		}
		s.create(w, r, id, hash, now)
		return
	}
	if sess == nil || subtle.ConstantTimeCompare(hash[:], sess.credHash[:]) != 1 {
		writeError(w, 404, "not_found", "not found")
		return
	}
	if isBase {
		if r.Method == http.MethodDelete {
			delete(s.sessions, id)
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, 200, sess.info(id))
		}
		return
	}
	if r.PathValue("index") != "" {
		s.chunk(w, r, sess)
		return
	}
	s.manifest(w, r, sess)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, id protocol.SessionID, hash [32]byte, now time.Time) {
	body, ok := readBody(w, r, 2<<20)
	if !ok {
		return
	}
	var request protocol.CreateSessionRequest
	if json.Unmarshal(body, &request) != nil || request.ChunkCount == 0 || request.ChunkCount > protocol.MaxChunkCount || request.TTLSeconds < 0 {
		writeError(w, 400, "bad_request", "invalid session request")
		return
	}
	if request.TTLSeconds > protocol.DefaultCaps.TTLMaxSeconds {
		writeError(w, 400, "ttl_too_long", "TTL exceeds cap")
		return
	}
	if request.TTLSeconds == 0 {
		request.TTLSeconds = protocol.DefaultCaps.TTLDefaultSeconds
	}
	sess := &session{credHash: hash, expiresAt: now.Add(time.Duration(request.TTLSeconds) * time.Second), chunkCount: request.ChunkCount, chunks: make([][]byte, request.ChunkCount)}
	s.sessions[id] = sess
	writeJSON(w, 201, sess.info(id))
}

func (sess *session) info(id protocol.SessionID) protocol.SessionInfo {
	return protocol.SessionInfo{SessionID: id.String(), ExpiresAt: sess.expiresAt, ChunkCount: sess.chunkCount, ChunksStored: sess.stored, ManifestStored: sess.manifest != nil}
}

func (s *Server) chunk(w http.ResponseWriter, r *http.Request, sess *session) {
	var body []byte
	if r.Method == http.MethodPut {
		var ok bool
		body, ok = readBody(w, r, protocol.ChunkSize+protocol.ChunkOverhead)
		if !ok {
			return
		}
	}
	index, err := strconv.ParseUint(r.PathValue("index"), 10, 64)
	if err != nil || index >= uint64(sess.chunkCount) {
		if r.Method == http.MethodGet {
			writeError(w, 404, "not_found", "not found")
		} else {
			writeError(w, 400, "bad_request", "invalid chunk index")
		}
		return
	}
	if r.Method == http.MethodGet {
		writeCiphertext(w, sess.chunks[index])
		return
	}
	if len(body) < 17 || (index < uint64(sess.chunkCount-1) && len(body) != protocol.ChunkSize+protocol.ChunkOverhead) {
		writeError(w, 400, "bad_request", "invalid chunk size")
		return
	}
	if sess.chunks[index] != nil {
		writeError(w, 409, "already_exists", "already exists")
		return
	}
	sess.chunks[index] = body
	sess.stored++
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request, sess *session) {
	if r.Method == http.MethodGet {
		writeCiphertext(w, sess.manifest)
		return
	}
	body, ok := readBody(w, r, protocol.MaxManifestBytes)
	if !ok {
		return
	}
	if sess.manifest != nil {
		writeError(w, 409, "already_exists", "already exists")
		return
	}
	if sess.stored != sess.chunkCount {
		writeError(w, 409, "chunks_missing", "chunks missing")
		return
	}
	sess.manifest = body
	w.WriteHeader(http.StatusCreated)
}

func readBody(w http.ResponseWriter, r *http.Request, capBytes int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, capBytes+1))
	if int64(len(body)) > capBytes {
		writeError(w, 413, "too_large", "body exceeds cap")
		return nil, false
	}
	if err != nil {
		writeError(w, 400, "bad_request", "cannot read body")
		return nil, false
	}
	return body, true
}

func writeCiphertext(w http.ResponseWriter, body []byte) {
	if body == nil {
		writeError(w, 404, "not_found", "not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.ErrorResponse{Error: code, Message: message})
}

// SPDX-License-Identifier: Apache-2.0
package relay

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/akenhq/aken/protocol"
)

type Options struct {
	Now  func() time.Time // nil means time.Now
	Caps protocol.Caps    // zero value means protocol.DefaultCaps
}

type handler struct {
	store Store
	opts  Options
	mux   *http.ServeMux
}

func NewHandler(store Store, opts Options) http.Handler {
	if opts.Caps == (protocol.Caps{}) {
		opts.Caps = protocol.DefaultCaps
	}
	s := &handler{store: store, opts: opts, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v0/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, protocol.Info{ProtocolVersions: []int{1}, Caps: s.opts.Caps})
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

func (s *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func (s *handler) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now().UTC()
}

func (s *handler) handleSession(w http.ResponseWriter, r *http.Request) {
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
	if !validCredential {
		writeError(w, 404, "not_found", "not found")
		return
	}
	sess, err := s.store.Session(r.Context(), id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		writeStoreError(w, err)
		return
	}
	now := s.now()
	if err == nil && !now.Before(sess.ExpiresAt) {
		_ = s.store.DeleteSession(r.Context(), id)
		err = ErrNotFound
	}
	hash := sha256.Sum256(credential[:])
	isBase := r.URL.Path == "/v0/sessions/"+id.String()
	if isBase && r.Method == http.MethodPut {
		if err == nil {
			writeError(w, 409, "already_exists", "already exists")
			return
		}
		s.create(w, r, id, hash, now)
		return
	}
	if err != nil || subtle.ConstantTimeCompare(hash[:], sess.CredentialHash[:]) != 1 {
		writeError(w, 404, "not_found", "not found")
		return
	}
	if isBase {
		if r.Method == http.MethodDelete {
			if err := s.store.DeleteSession(r.Context(), id); err != nil {
				writeStoreError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, 200, sess.info(id))
		}
		return
	}
	if r.PathValue("index") != "" {
		s.chunk(w, r, id, sess)
		return
	}
	s.manifest(w, r, id)
}

func (s *handler) create(w http.ResponseWriter, r *http.Request, id protocol.SessionID, hash [32]byte, now time.Time) {
	body, ok := readBody(w, r, 2<<20)
	if !ok {
		return
	}
	var request protocol.CreateSessionRequest
	if json.Unmarshal(body, &request) != nil || request.ChunkCount == 0 || int64(request.ChunkCount) > int64(s.opts.Caps.ChunkCount) || request.TTLSeconds < 0 {
		writeError(w, 400, "bad_request", "invalid session request")
		return
	}
	if request.TTLSeconds > s.opts.Caps.TTLMaxSeconds {
		writeError(w, 400, "ttl_too_long", "TTL exceeds cap")
		return
	}
	if request.TTLSeconds == 0 {
		request.TTLSeconds = s.opts.Caps.TTLDefaultSeconds
	}
	sess := SessionMeta{CredentialHash: hash, ExpiresAt: now.Add(time.Duration(request.TTLSeconds) * time.Second), ChunkCount: request.ChunkCount}
	if err := s.store.CreateSession(r.Context(), id, sess); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, 201, sess.info(id))
}

func (sess SessionMeta) info(id protocol.SessionID) protocol.SessionInfo {
	return protocol.SessionInfo{SessionID: id.String(), ExpiresAt: sess.ExpiresAt, ChunkCount: sess.ChunkCount, ChunksStored: sess.ChunksStored, ManifestStored: sess.ManifestStored}
}

func (s *handler) chunk(w http.ResponseWriter, r *http.Request, id protocol.SessionID, sess SessionMeta) {
	var body []byte
	if r.Method == http.MethodPut {
		var ok bool
		body, ok = readBody(w, r, s.opts.Caps.ChunkBytes)
		if !ok {
			return
		}
	}
	index, err := strconv.ParseUint(r.PathValue("index"), 10, 64)
	if err != nil || index >= uint64(sess.ChunkCount) {
		if r.Method == http.MethodGet {
			writeError(w, 404, "not_found", "not found")
		} else {
			writeError(w, 400, "bad_request", "invalid chunk index")
		}
		return
	}
	if r.Method == http.MethodGet {
		body, err := s.store.GetChunk(r.Context(), id, index)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeCiphertext(w, body)
		return
	}
	if len(body) < 17 || (index < uint64(sess.ChunkCount-1) && len(body) != protocol.ChunkSize+protocol.ChunkOverhead) {
		writeError(w, 400, "bad_request", "invalid chunk size")
		return
	}
	if err := s.store.PutChunk(r.Context(), id, index, body); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *handler) manifest(w http.ResponseWriter, r *http.Request, id protocol.SessionID) {
	if r.Method == http.MethodGet {
		body, err := s.store.GetManifest(r.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeCiphertext(w, body)
		return
	}
	body, ok := readBody(w, r, s.opts.Caps.ManifestBytes)
	if !ok {
		return
	}
	sess, err := s.store.Session(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if sess.ManifestStored {
		writeError(w, 409, "already_exists", "already exists")
		return
	}
	if sess.ChunksStored != sess.ChunkCount {
		writeError(w, 409, "chunks_missing", "chunks missing")
		return
	}
	if err := s.store.PutManifest(r.Context(), id, body); err != nil {
		writeStoreError(w, err)
		return
	}
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
	_, _ = w.Write(body) //nolint:gosec // opaque ciphertext served as application/octet-stream
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.ErrorResponse{Error: code, Message: message})
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, 404, "not_found", "not found")
	case errors.Is(err, ErrExists):
		writeError(w, 409, "already_exists", "already exists")
	default:
		writeError(w, 500, "internal", "internal error")
	}
}

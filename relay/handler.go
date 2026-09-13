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
	"strings"
	"time"

	"github.com/akenhq/aken/protocol"
)

type Options struct {
	Now  func() time.Time // nil means time.Now
	Caps protocol.Caps    // zero value means protocol.DefaultCaps
}

type Handler struct {
	live  registry
	store Store
	opts  Options
	mux   *http.ServeMux
}

func NewHandler(store Store, opts Options) *Handler {
	if opts.Caps == (protocol.Caps{}) {
		opts.Caps = protocol.DefaultCaps
	}
	s := &Handler{store: store, opts: opts, mux: http.NewServeMux(), live: registry{sessions: make(map[protocol.SessionID]*liveSession)}}
	s.mux.HandleFunc("GET /v0/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, protocol.Info{ProtocolVersions: []int{1}, Caps: s.opts.Caps})
	})
	for _, route := range []struct{ method, path string }{
		{"PUT", "/v0/sessions/{sid}"}, {"GET", "/v0/sessions/{sid}"}, {"DELETE", "/v0/sessions/{sid}"},
		{"PUT", "/v0/sessions/{sid}/blob/chunks/{index}"}, {"GET", "/v0/sessions/{sid}/blob/chunks/{index}"},
		{"PUT", "/v0/sessions/{sid}/blob/manifest"}, {"GET", "/v0/sessions/{sid}/blob/manifest"},
		{"POST", "/v0/sessions/{sid}/join"}, {"GET", "/v0/sessions/{sid}/join"},
		{"POST", "/v0/sessions/{sid}/jobs"}, {"GET", "/v0/sessions/{sid}/jobs"},
		{"POST", "/v0/sessions/{sid}/results"}, {"GET", "/v0/sessions/{sid}/results"},
	} {
		s.mux.HandleFunc(route.method+" "+route.path, s.handleSession)
	}
	for _, path := range []string{"/v0/info", "/v0/sessions/{sid}", "/v0/sessions/{sid}/blob/chunks/{index}", "/v0/sessions/{sid}/blob/manifest", "/v0/sessions/{sid}/join", "/v0/sessions/{sid}/jobs", "/v0/sessions/{sid}/results"} {
		s.mux.HandleFunc(path, methodNotAllowed)
	}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeError(w, 404, "not_found", "not found") })
	return s
}

func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeError(w, 405, "method_not_allowed", "method not allowed")
}

func (s *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func (s *Handler) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now().UTC()
}

func (s *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
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
		s.live.mu.Lock()
		sess, err = s.store.Session(r.Context(), id)
		if err == nil && !now.Before(sess.ExpiresAt) {
			_ = s.store.DeleteSession(r.Context(), id)
			s.live.dropLocked(id)
			err = ErrNotFound
		}
		s.live.mu.Unlock()
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
	var live *liveSession
	if sess.Mode == "session" {
		live = s.live.get(id)
		if live == nil {
			writeError(w, 404, "not_found", "not found")
			return
		}
	}
	if isBase {
		if r.Method == http.MethodDelete {
			s.live.mu.Lock()
			err := s.store.DeleteSession(r.Context(), id)
			if err == nil {
				s.live.dropLocked(id)
			}
			s.live.mu.Unlock()
			if err != nil {
				writeStoreError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		} else {
			info := sess.info(id)
			if live != nil {
				live.mu.Lock()
				info.Joined = live.joined != nil
				live.mu.Unlock()
			}
			writeJSON(w, 200, info)
		}
		return
	}
	isBlob := strings.HasPrefix(r.URL.Path, "/v0/sessions/"+id.String()+"/blob/")
	if isBlob == (sess.Mode == "session") {
		writeError(w, 409, "wrong_mode", "wrong session mode")
		return
	}
	if !isBlob {
		s.sessionRoute(w, r, id, sess, live)
		return
	}
	if r.PathValue("index") != "" {
		s.chunk(w, r, id, sess)
		return
	}
	s.manifest(w, r, id)
}

func (s *Handler) create(w http.ResponseWriter, r *http.Request, id protocol.SessionID, hash [32]byte, now time.Time) {
	body, ok := readBody(w, r, 2<<20)
	if !ok {
		return
	}
	var request protocol.CreateSessionRequest
	if json.Unmarshal(body, &request) != nil || request.TTLSeconds < 0 {
		writeError(w, 400, "bad_request", "invalid session request")
		return
	}
	if request.Mode == "" {
		request.Mode = "blob"
	}
	var collectorKey, collectorMAC [32]byte
	valid := false
	switch request.Mode {
	case "blob":
		valid = request.ChunkCount > 0 && int64(request.ChunkCount) <= int64(s.opts.Caps.ChunkCount)
	case "session":
		var keyOK, macOK bool
		collectorKey, keyOK = protocol.DecodeKey(request.CollectorKey)
		collectorMAC, macOK = protocol.DecodeKey(request.CollectorMAC)
		valid = request.ChunkCount == 0 && keyOK && macOK
	}
	if !valid {
		writeError(w, 400, "bad_request", "invalid session request")
		return
	}
	if request.TTLSeconds > s.opts.Caps.TTLMaxSeconds {
		writeError(w, 400, "ttl_too_long", "TTL exceeds cap")
		return
	}
	if request.TTLSeconds == 0 {
		request.TTLSeconds = s.opts.Caps.TTLDefaultSeconds
		if request.Mode == "session" {
			request.TTLSeconds = s.opts.Caps.SessionTTLDefaultSeconds
		}
	}
	sess := SessionMeta{Mode: request.Mode, CollectorKey: collectorKey, CollectorMAC: collectorMAC, CredentialHash: hash, ExpiresAt: now.Add(time.Duration(request.TTLSeconds) * time.Second), ChunkCount: request.ChunkCount}
	if err := s.store.CreateSession(r.Context(), id, sess); err != nil {
		writeStoreError(w, err)
		return
	}
	s.live.mu.Lock()
	s.live.dropLocked(id)
	if sess.Mode == "session" {
		s.live.sessions[id] = &liveSession{nextJob: 1, nextResult: 1, changed: make(chan struct{}), expiresAt: sess.ExpiresAt}
	}
	s.live.mu.Unlock()
	writeJSON(w, 201, sess.info(id))
}

func (sess SessionMeta) info(id protocol.SessionID) protocol.SessionInfo {
	info := protocol.SessionInfo{Mode: sess.Mode, SessionID: id.String(), ExpiresAt: sess.ExpiresAt, ChunkCount: sess.ChunkCount, ChunksStored: sess.ChunksStored, ManifestStored: sess.ManifestStored}
	if info.Mode == "" {
		info.Mode = "blob"
	}
	if info.Mode == "session" {
		info.CollectorKey = protocol.EncodeKey(sess.CollectorKey)
		info.CollectorMAC = protocol.EncodeKey(sess.CollectorMAC)
	}
	return info
}

func (s *Handler) chunk(w http.ResponseWriter, r *http.Request, id protocol.SessionID, sess SessionMeta) {
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

func (s *Handler) manifest(w http.ResponseWriter, r *http.Request, id protocol.SessionID) {
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

func (s *Handler) sessionRoute(w http.ResponseWriter, r *http.Request, id protocol.SessionID, sess SessionMeta, live *liveSession) {
	kind := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
	if r.Method == http.MethodGet {
		wait := 0
		values, present := r.URL.Query()["wait"]
		if present {
			var err error
			if len(values) != 1 {
				writeError(w, 400, "bad_request", "invalid wait")
				return
			}
			wait, err = strconv.Atoi(values[0])
			if err != nil || wait < 0 || wait > min(30, s.opts.Caps.WaitMaxSeconds) {
				writeError(w, 400, "bad_request", "invalid wait")
				return
			}
		}
		s.poll(w, r, id, live, kind, time.Duration(wait)*time.Second)
		return
	}
	if kind == "join" {
		s.join(w, r, sess, live)
		return
	}
	live.mu.Lock()
	joined, gone := live.joined != nil, live.gone
	live.mu.Unlock()
	if gone {
		writeError(w, 404, "not_found", "not found")
		return
	}
	if !joined {
		writeError(w, 409, "not_joined", "session not joined")
		return
	}
	capBytes := s.opts.Caps.JobBytes
	if kind == "results" {
		capBytes = s.opts.Caps.ResultBytes
	}
	// Leave room for base64 and JSON; the decoded ciphertext cap is checked below.
	body, ok := readBody(w, r, max(2<<20, 4*((capBytes+2)/3)+1024))
	if !ok {
		return
	}
	var e protocol.Envelope
	if json.Unmarshal(body, &e) != nil || e.Validate(id, len(e.Payload)) != nil {
		writeError(w, 400, "bad_request", "invalid envelope")
		return
	}
	if e.Class != protocol.ClassRead {
		writeError(w, 403, "class_not_allowed", "class not allowed")
		return
	}
	if int64(len(e.Payload)) > capBytes {
		writeError(w, 413, "too_large", "payload exceeds cap")
		return
	}
	status, code := live.post(e, kind == "results", s.opts.Caps.QueueLength)
	if code != "" {
		writeError(w, status, code, strings.ReplaceAll(code, "_", " "))
		return
	}
	w.WriteHeader(status)
}

func (s *Handler) join(w http.ResponseWriter, r *http.Request, sess SessionMeta, live *liveSession) {
	live.mu.Lock()
	gone, joined := live.gone, live.joined != nil
	live.mu.Unlock()
	if gone {
		writeError(w, 404, "not_found", "not found")
		return
	}
	if joined {
		writeError(w, 409, "already_exists", "already exists")
		return
	}
	body, ok := readBody(w, r, 2<<20)
	if !ok {
		return
	}
	var request protocol.JoinRequest
	if json.Unmarshal(body, &request) != nil {
		writeError(w, 400, "bad_request", "invalid join request")
		return
	}
	_, keyOK := protocol.DecodeKey(request.MCPKey)
	_, macOK := protocol.DecodeKey(request.MCPMAC)
	if !keyOK || !macOK || (request.Via != "cli" && request.Via != "chat") {
		writeError(w, 400, "bad_request", "invalid join request")
		return
	}
	live.mu.Lock()
	if live.gone {
		live.mu.Unlock()
		writeError(w, 404, "not_found", "not found")
		return
	}
	if live.joined != nil {
		live.mu.Unlock()
		writeError(w, 409, "already_exists", "already exists")
		return
	}
	live.joined = &protocol.JoinInfo{CollectorKey: protocol.EncodeKey(sess.CollectorKey), CollectorMAC: protocol.EncodeKey(sess.CollectorMAC), MCPKey: request.MCPKey, MCPMAC: request.MCPMAC, Via: request.Via, JoinedAt: s.now()}
	info := *live.joined
	live.notify()
	live.mu.Unlock()
	writeJSON(w, 201, info)
}

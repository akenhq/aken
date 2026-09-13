// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type RelayClient struct {
	BaseURL    string
	HTTP       *http.Client
	credential [32]byte
}

func NewRelayClient(baseURL string, credential [32]byte) (*RelayClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(baseURL, "#") || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("protocol: invalid relay URL")
	}
	loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, errors.New("protocol: invalid relay URL")
	}
	return &RelayClient{BaseURL: strings.TrimSuffix(u.String(), "/"), credential: credential}, nil
}

type RelayError struct {
	Status  int
	Code    string
	Message string
}

func (e *RelayError) Error() string {
	return fmt.Sprintf("relay: %d %s: %s", e.Status, e.Code, e.Message)
}

func IsNotFound(err error) bool {
	var relayErr *RelayError
	return errors.As(err, &relayErr) && relayErr.Status == http.StatusNotFound
}

func (c *RelayClient) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := build()
		if err != nil {
			return nil, errors.New("protocol: cannot build relay request")
		}
		response, err := client.Do(req)
		if err == nil && (response.StatusCode < 500 || response.StatusCode >= 600 || attempt == 2) {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return response, nil
			}
			defer func() { _ = response.Body.Close() }()
			relayErr := &RelayError{Status: response.StatusCode, Code: "http_" + strconv.Itoa(response.StatusCode)}
			body, readErr := readRelayBody(response.Body, 2<<20)
			var wire ErrorResponse
			if readErr == nil && json.Unmarshal(body, &wire) == nil && wire.Error != "" {
				relayErr.Code = wire.Error
				var message strings.Builder
				for _, r := range wire.Message {
					if !unicode.IsPrint(r) && r != ' ' {
						r = utf8.RuneError
					}
					if message.Len()+utf8.RuneLen(r) > 200 {
						break
					}
					message.WriteRune(r)
				}
				relayErr.Message = message.String()
			}
			return nil, relayErr
		}
		if response != nil {
			_ = response.Body.Close()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt == 2 {
			return nil, fmt.Errorf("protocol: relay transport failed: %w", err)
		}
		delay := 500 * time.Millisecond
		if attempt == 1 {
			delay = 2 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

func readRelayBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, errors.New("protocol: cannot read relay response")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("protocol: relay response too large")
	}
	return data, nil
}

func (c *RelayClient) request(ctx context.Context, method, path, contentType string, body []byte, limit int64) ([]byte, error) {
	response, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set(ProtocolHeader, strconv.Itoa(ProtocolVersion))
		req.Header.Set("Authorization", AuthorizationHeader(c.credential))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	return readRelayBody(response.Body, limit)
}

func sessionPath(id SessionID) string { return "/v0/sessions/" + id.String() }
func chunkPath(id SessionID, index uint64) string {
	return sessionPath(id) + "/blob/chunks/" + strconv.FormatUint(index, 10)
}

func (c *RelayClient) Info(ctx context.Context) (Info, error) {
	var info Info
	body, err := c.request(ctx, http.MethodGet, "/v0/info", "", nil, 2<<20)
	if err == nil {
		err = decodeRelayJSON(body, &info)
	}
	return info, err
}

func decodeRelayJSON(body []byte, value any) error {
	if err := json.Unmarshal(body, value); err != nil {
		return errors.New("protocol: invalid relay JSON")
	}
	return nil
}

func (c *RelayClient) CreateSession(ctx context.Context, id SessionID, ttl time.Duration, chunkCount uint32) (SessionInfo, error) {
	var info SessionInfo
	body, err := json.Marshal(CreateSessionRequest{TTLSeconds: int64(ttl / time.Second), ChunkCount: chunkCount})
	if err != nil {
		return info, err
	}
	body, err = c.request(ctx, http.MethodPut, sessionPath(id), "application/json", body, 2<<20)
	if err == nil {
		err = decodeRelayJSON(body, &info)
	}
	return info, err
}

func (c *RelayClient) Session(ctx context.Context, id SessionID) (SessionInfo, error) {
	var info SessionInfo
	body, err := c.request(ctx, http.MethodGet, sessionPath(id), "", nil, 2<<20)
	if err == nil {
		err = decodeRelayJSON(body, &info)
	}
	return info, err
}

func (c *RelayClient) PutChunk(ctx context.Context, id SessionID, index uint64, ciphertext []byte) error {
	_, err := c.request(ctx, http.MethodPut, chunkPath(id, index), "application/octet-stream", ciphertext, 2<<20)
	var relayErr *RelayError
	if errors.As(err, &relayErr) && relayErr.Status == http.StatusConflict && relayErr.Code == "already_exists" {
		return nil
	}
	return err
}

func (c *RelayClient) PutManifest(ctx context.Context, id SessionID, ciphertext []byte) error {
	_, err := c.request(ctx, http.MethodPut, sessionPath(id)+"/blob/manifest", "application/octet-stream", ciphertext, 2<<20)
	return err
}

func (c *RelayClient) GetManifest(ctx context.Context, id SessionID) ([]byte, error) {
	return c.request(ctx, http.MethodGet, sessionPath(id)+"/blob/manifest", "", nil, ChunkSize+ChunkOverhead)
}

func (c *RelayClient) GetChunk(ctx context.Context, id SessionID, index uint64) ([]byte, error) {
	return c.request(ctx, http.MethodGet, chunkPath(id, index), "", nil, ChunkSize+ChunkOverhead)
}

func (c *RelayClient) DeleteSession(ctx context.Context, id SessionID) error {
	_, err := c.request(ctx, http.MethodDelete, sessionPath(id), "", nil, 2<<20)
	return err
}

func (c *RelayClient) CreatePersistentSession(ctx context.Context, id SessionID, ttl time.Duration, collectorKey, collectorMAC [32]byte) (SessionInfo, error) {
	var info SessionInfo
	body, err := json.Marshal(CreateSessionRequest{TTLSeconds: int64(ttl / time.Second), Mode: "session", CollectorKey: EncodeKey(collectorKey), CollectorMAC: EncodeKey(collectorMAC)})
	if err != nil {
		return info, err
	}
	body, err = c.request(ctx, http.MethodPut, sessionPath(id), "application/json", body, 2<<20)
	if err == nil {
		err = decodeRelayJSON(body, &info)
	}
	return info, err
}

func (c *RelayClient) Join(ctx context.Context, id SessionID, mcpKey, mcpMAC [32]byte, via string) (JoinInfo, error) {
	var info JoinInfo
	body, err := json.Marshal(JoinRequest{MCPKey: EncodeKey(mcpKey), MCPMAC: EncodeKey(mcpMAC), Via: via})
	if err != nil {
		return info, err
	}
	body, err = c.request(ctx, http.MethodPost, sessionPath(id)+"/join", "application/json", body, 2<<20)
	if err == nil {
		err = decodeRelayJSON(body, &info)
	}
	return info, err
}

func waitQuery(wait time.Duration) string {
	return "?wait=" + strconv.FormatInt(int64(max(0, min(wait/time.Second, 30))), 10)
}

func (c *RelayClient) WaitJoin(ctx context.Context, id SessionID, wait time.Duration) (JoinInfo, bool, error) {
	var info JoinInfo
	body, err := c.request(ctx, http.MethodGet, sessionPath(id)+"/join"+waitQuery(wait), "", nil, 2<<20)
	if err != nil || len(body) == 0 {
		return info, false, err
	}
	err = decodeRelayJSON(body, &info)
	return info, err == nil, err
}

func (c *RelayClient) postEnvelope(ctx context.Context, id SessionID, suffix string, e Envelope) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = c.request(ctx, http.MethodPost, sessionPath(id)+suffix, "application/json", body, 2<<20)
	return err
}

func (c *RelayClient) PostJob(ctx context.Context, id SessionID, e Envelope) error {
	return c.postEnvelope(ctx, id, "/jobs", e)
}
func (c *RelayClient) PostResult(ctx context.Context, id SessionID, e Envelope) error {
	return c.postEnvelope(ctx, id, "/results", e)
}

func (c *RelayClient) pollMessages(ctx context.Context, id SessionID, suffix string, wait time.Duration, capBytes int) ([]Envelope, error) {
	// A poll can return 64 base64-encoded ciphertexts, plus their JSON envelopes.
	limit := int64(64 * (4*((capBytes+2)/3) + 1024))
	body, err := c.request(ctx, http.MethodGet, sessionPath(id)+suffix+waitQuery(wait), "", nil, limit)
	if err != nil || len(body) == 0 {
		return nil, err
	}
	var messages Messages
	if err := decodeRelayJSON(body, &messages); err != nil {
		return nil, err
	}
	return messages.Messages, nil
}

func (c *RelayClient) PollJobs(ctx context.Context, id SessionID, wait time.Duration) ([]Envelope, error) {
	return c.pollMessages(ctx, id, "/jobs", wait, MaxJobBytes)
}
func (c *RelayClient) PollResults(ctx context.Context, id SessionID, wait time.Duration) ([]Envelope, error) {
	return c.pollMessages(ctx, id, "/results", wait, MaxResultBytes)
}

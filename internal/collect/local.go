// SPDX-License-Identifier: Apache-2.0
package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/akenhq/aken/protocol"
)

type runInfo struct {
	SessionID  string    `json:"session_id"`
	Relay      string    `json:"relay"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	ChunkCount uint32    `json:"chunk_count"`
	Argv       []string  `json:"argv"`
}

func writeLocalCopy(dir string, plaintext []byte, m protocol.Manifest, mapping map[string]string, run runInfo) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mapped, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		return err
	}
	info, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, f := range []struct {
		name string
		data []byte
	}{{"artifact.txt", plaintext}, {"manifest.json", manifest}, {"mapping.json", mapped}, {"run.json", info}} {
		file, err := root.OpenFile(f.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = file.Write(f.data)
		if err == nil {
			err = file.Chmod(0o400)
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

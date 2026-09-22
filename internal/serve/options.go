// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/akenhq/aken/internal/collect"
	"github.com/akenhq/aken/protocol"
)

type Options struct {
	Level                       int
	TTL                         time.Duration
	RelayURL                    string
	Allow, Keep, KeepCategories []string
	RulesFile, StateDir         string
	Retention                   time.Duration
	Argv                        []string
	Now                         func() time.Time
	Collector                   string
}

func validate(o Options) error {
	if o.Level != 0 && o.Level != 1 {
		return errors.New("--level must be 0 or 1")
	}
	if o.TTL <= 0 || o.TTL > protocol.MaxTTL {
		return errors.New("--ttl must be greater than 0 and at most 24h")
	}
	if o.Retention < 0 {
		return errors.New("--retention must not be negative")
	}
	for _, dir := range o.Allow {
		if !filepath.IsAbs(dir) {
			return errors.New("--allow directory must be absolute")
		}
	}
	if err := collect.ValidateKeepCategories(o.KeepCategories); err != nil {
		return err
	}
	_, err := protocol.NewRelayClient(o.RelayURL, [32]byte{})
	return err
}

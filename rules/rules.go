// SPDX-License-Identifier: Apache-2.0

// Package rules holds the default redaction rules as data.
package rules

import _ "embed"

//go:embed default.json
var Default []byte

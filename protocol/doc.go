// SPDX-License-Identifier: Apache-2.0

// Package protocol implements the wire protocol shared by the collector, the
// local MCP and the relays. It currently implements tokens and key derivation;
// envelopes, the blob format and the relay API client come later. The normative
// text lives in spec/. This package is the audit target and uses only the standard
// library.
package protocol

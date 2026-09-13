// SPDX-License-Identifier: Apache-2.0
package collect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/akenhq/aken/internal/redact"
	"github.com/akenhq/aken/internal/screen"
	"github.com/akenhq/aken/internal/source"
	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/rules"
)

func validate(o Options) error {
	if len(o.Units)+len(o.Containers)+len(o.Files)+len(o.Globs) == 0 {
		return errors.New("at least one source is required")
	}
	for _, name := range o.Units {
		if err := source.ValidateUnit(name); err != nil {
			return err
		}
	}
	for _, name := range o.Containers {
		if err := source.ValidateContainer(name); err != nil {
			return err
		}
	}
	if !o.Since.Before(o.Until) {
		return errors.New("--since must be before --until")
	}
	if o.TTL <= 0 || o.TTL > protocol.MaxTTL {
		return errors.New("--ttl must be greater than 0 and at most 24h")
	}
	if o.Tail < 0 {
		return errors.New("--tail must not be negative")
	}
	if o.Retention < 0 {
		return errors.New("--retention must not be negative")
	}
	for _, dir := range o.Allow {
		if !filepath.IsAbs(dir) {
			return errors.New("--allow directory must be absolute")
		}
	}
	_, err := protocol.NewRelayClient(o.RelayURL, [32]byte{})
	return err
}
func LoadRules(rulesFile string, keep, keepCategories []string) (*redact.Engine, redact.File, string, int, int, error) {
	base, err := redact.ParseRules(rules.Default)
	if err != nil {
		return nil, redact.File{}, "", 0, 0, err
	}
	path := rulesFile
	if path == "" {
		if _, err := os.Stat("/etc/aken/rules.json"); err == nil {
			path = "/etc/aken/rules.json"
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, redact.File{}, "", 0, 0, err
		}
	}
	merged := base
	var extra redact.File
	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // the user selects the local rules file
		if err != nil {
			return nil, redact.File{}, "", 0, 0, err
		}
		extra, err = redact.ParseRules(data)
		if err != nil {
			return nil, redact.File{}, "", 0, 0, err
		}
		merged, err = redact.Merge(base, extra)
		if err != nil {
			return nil, redact.File{}, "", 0, 0, err
		}
	}
	engine, err := redact.Compile(merged, keep, keepCategories)
	defaults, extras := 0, 0
	for _, r := range merged.Rules {
		if r.Enabled != nil && !*r.Enabled || slices.Contains(keepCategories, r.Category) {
			continue
		}
		if slices.ContainsFunc(extra.Rules, func(x redact.Rule) bool { return x.Name == r.Name }) {
			extras++
		} else {
			defaults++
		}
	}
	return engine, merged, path, defaults, extras, err
}
func readSources(ctx context.Context, o Options, stderr io.Writer) ([]*source.Source, error) {
	var specs []source.Spec
	for _, name := range o.Units {
		specs = append(specs, source.Spec{Kind: source.KindUnit, Target: name})
	}
	for _, name := range o.Containers {
		names, err := source.ResolveContainers(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, target := range names {
			match := "CONTAINER_NAME=" + target
			if len(name) == 12 || len(name) == 64 {
				if _, err := hex.DecodeString(name); err == nil {
					match = "CONTAINER_ID=" + name
					if len(name) == 64 {
						match = "CONTAINER_ID_FULL=" + name
					}
				}
			}
			specs = append(specs, source.Spec{Kind: source.KindContainer, Target: target, Match: match})
		}
	}
	var sources []*source.Source
	for _, spec := range specs {
		s, err := source.ReadJournal(ctx, spec, o.Since, o.Until)
		if err != nil {
			return nil, err
		}
		if len(s.Lines) == 0 {
			_, _ = fmt.Fprintf(stderr, "aken: no journal entries for %s in the window; check the name, the window, that the aken user is in the systemd-journal group and, for containers, that the logging driver is journald\n", spec.Target)
		}
		sources = append(sources, s)
	}
	files := source.Files{Allowed: append([]string{"/var/log"}, o.Allow...), Tail: o.Tail, ModifiedAfter: o.Since}
	fileSources, err := source.ReadFiles(files, o.Files, o.Globs)
	if err != nil {
		return nil, err
	}
	sources = append(sources, fileSources...)
	return sources, nil
}
func Run(ctx context.Context, o Options, stdin io.Reader, stdout, stderr io.Writer, interactive bool, pageLines int) int {
	fail := func(err error) int { _, _ = fmt.Fprintf(stderr, "aken: %s\n", err); return 1 }
	if err := validate(o); err != nil {
		return fail(err)
	}
	engine, merged, path, defaults, extras, err := LoadRules(o.RulesFile, o.Keep, o.KeepCategories)
	if err != nil {
		return fail(err)
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	created := now().UTC()
	if o.StateDir == "" {
		o.StateDir = DefaultStateDir()
	}
	if !o.DryRun && o.Retention > 0 {
		if err := Prune(filepath.Join(o.StateDir, "runs"), created.Add(-o.Retention)); err != nil {
			return fail(err)
		}
	}
	sources, err := readSources(ctx, o, stderr)
	if err != nil {
		return fail(err)
	}
	screen := screenData{options: o, rulesPath: path, defaultRules: defaults, extraRules: extras}
	for _, r := range merged.Rules {
		if r.Enabled == nil || *r.Enabled {
			screen.categories = append(screen.categories, r.Category)
		}
	}
	m := protocol.Manifest{Version: protocol.BlobVersion, CreatedAt: created, Collector: o.Collector, ChunkSize: protocol.ChunkSize, Redaction: protocol.RedactionSummary{ByCategory: map[string]protocol.CategoryCount{}}}
	var plaintext bytes.Buffer
	var totalLines int64
	uniqueFlags := map[string]bool{}
	idFlags := map[string]bool{}
	for _, src := range sources {
		result := engine.Redact(src.Lines)
		screen.linesCollapsed += result.LinesCollapsed
		ms := protocol.ManifestSource{Name: src.Name, Kind: string(src.Kind), Target: src.Target, Offset: int64(plaintext.Len()), Lines: int64(len(result.Lines)), LinesRedacted: result.LinesRedacted, Note: src.Note}
		if src.Kind != source.KindFile {
			ms.Since = src.Since.UTC().Format(time.RFC3339)
			ms.Until = src.Until.UTC().Format(time.RFC3339)
		}
		for _, line := range result.Lines {
			plaintext.Write(line)
			plaintext.WriteByte('\n')
		}
		ms.Bytes = int64(plaintext.Len()) - ms.Offset
		m.Sources = append(m.Sources, ms)
		totalLines += ms.Lines
		m.Redaction.LinesRedacted += result.LinesRedacted
		m.Redaction.Rules = result.Rules
		for c, count := range result.ByCategory {
			previous := m.Redaction.ByCategory[c]
			previous.Values = count.Values
			previous.Lines += count.Lines
			m.Redaction.ByCategory[c] = previous
		}
		for _, f := range result.Flags {
			if f.IDShaped {
				idFlags[f.Value] = true
			} else {
				uniqueFlags[f.Value] = true
			}
		}
		screen.sources = append(screen.sources, screenSource{manifest: ms, lines: result.Lines, flags: result.Flags})
	}
	m.Redaction.Flags = int64(len(uniqueFlags))
	screen.idFlags = int64(len(idFlags))
	m.TotalBytes = int64(plaintext.Len())
	if m.TotalBytes > protocol.MaxChunkCount*protocol.ChunkSize {
		return fail(fmt.Errorf("%d exceeds the 128 MiB artifact cap; narrow --since or use --tail", m.TotalBytes))
	}
	if totalLines == 0 {
		return fail(errors.New("no log lines collected"))
	}
	m.ChunkCount, err = protocol.ChunkCount(m.TotalBytes)
	if err != nil {
		return fail(err)
	}
	screen.manifest = m
	if !interactive {
		renderScreen(stdout, screen)
		if o.DryRun {
			return 0
		}
		return fail(errors.New("the review screen needs a terminal"))
	}
	choice, err := review(bufio.NewReader(stdin), stdout, screen, o.DryRun, pageLines)
	if err != nil {
		return fail(err)
	}
	if o.DryRun {
		return 0
	}
	if choice == 'a' {
		_, _ = fmt.Fprintln(stderr, "aken: aborted, nothing was uploaded")
		return 3
	}
	return upload(ctx, o, plaintext.Bytes(), m, engine.Mapping(), stdout, stderr)
}
func upload(ctx context.Context, o Options, plaintext []byte, m protocol.Manifest, mapping map[string]string, stdout, stderr io.Writer) int {
	fail := func(err error) int { _, _ = fmt.Fprintf(stderr, "aken: %s\n", err); return 1 }
	token := protocol.NewToken()
	defer token.Zero()
	id := token.SessionID()
	keys := protocol.DeriveBlobKeys(token.ContentRoot())
	client, err := protocol.NewRelayClient(o.RelayURL, token.RelayCredential())
	if err != nil {
		return fail(err)
	}
	info, err := client.Info(ctx)
	if err != nil {
		return fail(err)
	}
	if int64(info.Caps.ChunkCount) < int64(m.ChunkCount) {
		return fail(errors.New("relay chunk cap is below the required chunk count"))
	}
	if info.Caps.TTLMaxSeconds < int64(o.TTL/time.Second) {
		return fail(errors.New("relay TTL cap is below the requested TTL"))
	}
	_, _ = fmt.Fprintf(stdout, "Creating the session on %s...\n", o.RelayURL)
	session, err := client.CreateSession(ctx, id, o.TTL, m.ChunkCount)
	if err != nil {
		return fail(err)
	}
	dir := filepath.Join(o.StateDir, "runs", m.CreatedAt.Format("20060102T150405Z")+"-"+id.String()[:8])
	uploadFail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "aken: upload failed: %s\nThe local copy is at %s; nothing readable reached the relay\n", err, dir)
		return 1
	}
	// The local manifest needs the ciphertext hashes before any files are written.
	chunks := protocol.SplitChunks(plaintext)
	sealed := make([][]byte, len(chunks))
	for i, chunk := range chunks {
		sealed[i], err = keys.SealChunk(uint64(i), m.ChunkCount, chunk)
		if err != nil {
			return uploadFail(err)
		}
		hash := sha256.Sum256(sealed[i])
		m.ChunksSHA256 = append(m.ChunksSHA256, hex.EncodeToString(hash[:]))
	}
	run := runInfo{SessionID: id.String(), Relay: o.RelayURL, CreatedAt: m.CreatedAt, ExpiresAt: session.ExpiresAt, ChunkCount: m.ChunkCount, Argv: o.Argv}
	if err := writeLocalCopy(dir, plaintext, m, mapping, run); err != nil {
		return uploadFail(err)
	}
	var uploaded int64
	for i, chunk := range sealed {
		uploaded += int64(len(chunks[i]))
		_, _ = fmt.Fprintf(stdout, "Uploading chunk %d of %d (%s of %s)...\n", i+1, m.ChunkCount, screen.SizeText(uploaded), screen.SizeText(m.TotalBytes))
		if err := client.PutChunk(ctx, id, uint64(i), chunk); err != nil {
			return uploadFail(err)
		}
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return uploadFail(err)
	}
	encrypted, err := keys.SealManifest(m.ChunkCount, manifest)
	if err != nil {
		return uploadFail(err)
	}
	_, _ = fmt.Fprintln(stdout, "Uploading the manifest...")
	if err := client.PutManifest(ctx, id, encrypted); err != nil {
		return uploadFail(err)
	}
	_, _ = fmt.Fprintf(stdout, "Uploaded %d %s (%s). The artifact expires at %s.\nLocal copy: %s\n\nSession token. Paste it into `aken-mcp join` on your machine, not into the agent chat:\n\n  %s\n", m.ChunkCount, screen.Plural(int64(m.ChunkCount), "chunk", "chunks"), screen.SizeText(m.TotalBytes), session.ExpiresAt.UTC().Format(time.RFC3339), dir, token.Encode())
	if o.RelayURL != protocol.DefaultRelayURL {
		_, _ = fmt.Fprintf(stdout, "\nWhen the relay is not the default, join with: aken-mcp join --relay %s\n", o.RelayURL)
	}
	return 0
}

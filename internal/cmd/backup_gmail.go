package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/gmail/v1"

	"github.com/steipete/gogcli/internal/backup"
	gmailbackup "github.com/steipete/gogcli/internal/backup/gmail"
	"github.com/steipete/gogcli/internal/config"
	"github.com/steipete/gogcli/internal/ui"
)

type gmailBackupOptions struct {
	Query            string
	Max              int64
	IncludeSpamTrash bool
	ShardMaxRows     int
	AccountHash      string
	CacheMessages    bool
	RefreshCache     bool
	Checkpoints      bool
	CheckpointRows   int
	CheckpointEvery  time.Duration
	CheckpointRunID  string
	BackupOptions    backup.Options
	Cache            gmailbackup.Cache
}

type gmailBackupMessage = gmailbackup.Message

type gmailBackupLabel struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Type                  string `json:"type,omitempty"`
	MessageListVisibility string `json:"messageListVisibility,omitempty"`
	LabelListVisibility   string `json:"labelListVisibility,omitempty"`
	MessagesTotal         int64  `json:"messagesTotal,omitempty"`
	MessagesUnread        int64  `json:"messagesUnread,omitempty"`
	ThreadsTotal          int64  `json:"threadsTotal,omitempty"`
	ThreadsUnread         int64  `json:"threadsUnread,omitempty"`
}

type gmailBackupFetchResult struct {
	id    string
	cache bool
	err   error
}

func buildGmailBackupSnapshot(ctx context.Context, flags *RootFlags, opts gmailBackupOptions) (backup.Snapshot, error) {
	if opts.ShardMaxRows <= 0 {
		opts.ShardMaxRows = 1000
	}
	account, err := requireAccount(flags)
	if err != nil {
		return backup.Snapshot{}, err
	}
	svc, err := gmailService(ctx, account)
	if err != nil {
		return backup.Snapshot{}, err
	}
	accountHash := backupAccountHash(account)
	opts.AccountHash = accountHash
	labels, err := fetchGmailBackupLabels(ctx, svc)
	if err != nil {
		return backup.Snapshot{}, err
	}
	if opts.CacheMessages {
		if cacheErr := configureGmailBackupCache(ctx, &opts); cacheErr != nil {
			return backup.Snapshot{}, cacheErr
		}
	}
	shards := make([]backup.PlainShard, 0, 1)
	labelShard, err := backup.NewJSONLShard(backupServiceGmail, "labels", accountHash, fmt.Sprintf("data/gmail/%s/labels.jsonl.gz.age", accountHash), labels)
	if err != nil {
		return backup.Snapshot{}, err
	}
	shards = append(shards, labelShard)
	var messageCount int
	if opts.CacheMessages {
		ids, listErr := listGmailBackupMessageIDs(ctx, svc, opts)
		if listErr != nil {
			return backup.Snapshot{}, listErr
		}
		opts.CheckpointRunID = gmailBackupResolvedCheckpointRunID(ctx, opts, ids)
		if cacheErr := ensureGmailBackupMessageCache(ctx, svc, opts, ids); cacheErr != nil {
			return backup.Snapshot{}, cacheErr
		}
		messageShards, promoted, shardErr := buildGmailMessageShardsFromCheckpoint(ctx, opts, ids)
		if shardErr != nil {
			return backup.Snapshot{}, shardErr
		}
		if !promoted {
			messageShards, shardErr = gmailbackup.BuildMessageShards(ctx, opts.Cache, ids, gmailbackup.ShardOptions{
				AccountHash: opts.AccountHash,
				MaxRows:     opts.ShardMaxRows,
				Progress: func(event gmailbackup.ShardEvent) {
					switch event.Phase {
					case "index":
						gmailBackupProgressf(ctx, "backup gmail shard-index\t%d/%d", event.Done, event.Total)
					case "build":
						gmailBackupProgressf(ctx, "backup gmail shard-build\tshards=%d\tmessages=%d/%d", event.Shards, event.Done, event.Total)
					}
				},
			})
			if shardErr != nil {
				return backup.Snapshot{}, shardErr
			}
		}
		shards = append(shards, messageShards...)
		messageCount = len(ids)
	} else {
		messages, err := fetchGmailBackupMessages(ctx, svc, opts)
		if err != nil {
			return backup.Snapshot{}, err
		}
		messageShards, err := gmailbackup.BuildMessageShardsFromMessages(ctx, messages, gmailbackup.ShardOptions{
			AccountHash: accountHash,
			MaxRows:     opts.ShardMaxRows,
		})
		if err != nil {
			return backup.Snapshot{}, err
		}
		shards = append(shards, messageShards...)
		messageCount = len(messages)
	}
	return backup.Snapshot{
		Services: []string{backupServiceGmail},
		Accounts: []string{accountHash},
		Counts: map[string]int{
			"gmail.labels":   len(labels),
			"gmail.messages": messageCount,
		},
		Shards: shards,
	}, nil
}

func configureGmailBackupCache(ctx context.Context, opts *gmailBackupOptions) error {
	if opts == nil || !opts.CacheMessages || opts.Cache.Configured() {
		return nil
	}
	layout, err := commandLayout(ctx, config.PathKindCache)
	if err != nil {
		return err
	}
	cache, err := gmailbackup.NewCache(layout.CacheDir)
	if err != nil {
		return err
	}
	opts.Cache = cache
	return nil
}

func fetchGmailBackupLabels(ctx context.Context, svc *gmail.Service) ([]gmailBackupLabel, error) {
	resp, err := svc.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	out := make([]gmailBackupLabel, 0, len(resp.Labels))
	for _, label := range resp.Labels {
		if label == nil {
			continue
		}
		out = append(out, gmailBackupLabel{
			ID:                    label.Id,
			Name:                  label.Name,
			Type:                  label.Type,
			MessageListVisibility: label.MessageListVisibility,
			LabelListVisibility:   label.LabelListVisibility,
			MessagesTotal:         label.MessagesTotal,
			MessagesUnread:        label.MessagesUnread,
			ThreadsTotal:          label.ThreadsTotal,
			ThreadsUnread:         label.ThreadsUnread,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func fetchGmailBackupMessages(ctx context.Context, svc *gmail.Service, opts gmailBackupOptions) ([]gmailBackupMessage, error) {
	ids, err := listGmailBackupMessageIDs(ctx, svc, opts)
	if err != nil {
		return nil, err
	}
	if !opts.CacheMessages {
		return fetchGmailBackupMessagesDirect(ctx, svc, ids)
	}
	if err := ensureGmailBackupMessageCache(ctx, svc, opts, ids); err != nil {
		return nil, err
	}
	ordered := make([]gmailBackupMessage, 0, len(ids))
	for _, id := range ids {
		msg, ok, err := opts.Cache.ReadMessage(opts.AccountHash, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("gmail message %s missing from backup cache", id)
		}
		ordered = append(ordered, msg)
	}
	return ordered, nil
}

func fetchGmailBackupMessagesDirect(ctx context.Context, svc *gmail.Service, ids []string) ([]gmailBackupMessage, error) {
	gmailBackupProgressf(ctx, "backup gmail fetch\tqueued=%d", len(ids))
	out := make([]gmailBackupMessage, 0, len(ids))
	for i, id := range ids {
		msg, err := svc.Users.Messages.Get("me", id).
			Format(gmailFormatRaw).
			Fields("id,threadId,historyId,internalDate,labelIds,sizeEstimate,raw").
			Context(ctx).
			Do()
		if err != nil {
			return nil, fmt.Errorf("gmail message %s: %w", id, err)
		}
		if strings.TrimSpace(msg.Raw) == "" {
			return nil, fmt.Errorf("gmail message %s returned empty raw payload", id)
		}
		out = append(out, gmailBackupMessage{
			ID:           msg.Id,
			ThreadID:     msg.ThreadId,
			HistoryID:    formatHistoryID(msg.HistoryId),
			InternalDate: msg.InternalDate,
			LabelIDs:     append([]string(nil), msg.LabelIds...),
			SizeEstimate: msg.SizeEstimate,
			Raw:          msg.Raw,
		})
		done := i + 1
		if done == len(ids) || done%100 == 0 {
			gmailBackupProgressf(ctx, "backup gmail fetch\t%d/%d\tfetched=%d\tcache=0", done, len(ids), done)
		}
	}
	return out, nil
}

func ensureGmailBackupMessageCache(ctx context.Context, svc *gmail.Service, opts gmailBackupOptions, ids []string) error {
	gmailBackupProgressf(ctx, "backup gmail fetch\tqueued=%d", len(ids))
	checkpointer := newGmailBackupCheckpointer(ctx, opts, len(ids))
	const maxConcurrency = 2
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan string)
	results := make(chan gmailBackupFetchResult, maxConcurrency)
	var wg sync.WaitGroup
	for range maxConcurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case messageID, ok := <-jobs:
					if !ok {
						return
					}
					results <- fetchGmailBackupMessageCacheResult(ctx, svc, opts, messageID)
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, id := range ids {
			select {
			case <-ctx.Done():
				return
			case jobs <- id:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	var firstErr error
	done := 0
	cacheHits := 0
	fetched := 0
	for res := range results {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
				cancel()
			}
			continue
		}
		done++
		if res.cache {
			cacheHits++
		} else if res.err == nil {
			fetched++
		}
		if err := checkpointer.record(ctx, res.id, done, fetched, cacheHits); err != nil {
			firstErr = err
			cancel()
			continue
		}
		if done == len(ids) || done%100 == 0 {
			gmailBackupProgressf(ctx, "backup gmail fetch\t%d/%d\tfetched=%d\tcache=%d", done, len(ids), fetched, cacheHits)
		}
		if done%1000 == 0 {
			debug.FreeOSMemory()
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if done != len(ids) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("gmail backup fetch stopped after %d/%d messages", done, len(ids))
	}
	return checkpointer.flush(ctx, done, fetched, cacheHits)
}

func fetchGmailBackupMessageCacheResult(ctx context.Context, svc *gmail.Service, opts gmailBackupOptions, messageID string) gmailBackupFetchResult {
	if opts.CacheMessages && !opts.RefreshCache {
		_, ok, err := opts.Cache.ReadMessage(opts.AccountHash, messageID)
		if err != nil {
			return gmailBackupFetchResult{id: messageID, err: err}
		}
		if ok {
			return gmailBackupFetchResult{id: messageID, cache: true}
		}
	}
	msg, err := svc.Users.Messages.Get("me", messageID).
		Format(gmailFormatRaw).
		Fields("id,threadId,historyId,internalDate,labelIds,sizeEstimate,raw").
		Context(ctx).
		Do()
	if err != nil {
		return gmailBackupFetchResult{id: messageID, err: fmt.Errorf("gmail message %s: %w", messageID, err)}
	}
	if strings.TrimSpace(msg.Raw) == "" {
		return gmailBackupFetchResult{id: messageID, err: fmt.Errorf("gmail message %s returned empty raw payload", messageID)}
	}
	backupMsg := gmailBackupMessage{
		ID:           msg.Id,
		ThreadID:     msg.ThreadId,
		HistoryID:    formatHistoryID(msg.HistoryId),
		InternalDate: msg.InternalDate,
		LabelIDs:     append([]string(nil), msg.LabelIds...),
		SizeEstimate: msg.SizeEstimate,
		Raw:          msg.Raw,
	}
	if opts.CacheMessages {
		if err := opts.Cache.WriteMessage(opts.AccountHash, backupMsg); err != nil {
			return gmailBackupFetchResult{id: messageID, err: err}
		}
	}
	return gmailBackupFetchResult{id: messageID}
}

type gmailBackupCheckpointer struct {
	enabled bool
	opts    gmailBackupOptions
	total   int
	part    int
	last    time.Time
	pending []string
}

const (
	gmailBackupShardKindMessages = gmailbackup.MessageShardKind
)

func newGmailBackupCheckpointer(ctx context.Context, opts gmailBackupOptions, total int) *gmailBackupCheckpointer {
	enabled := opts.Checkpoints &&
		opts.CacheMessages &&
		strings.TrimSpace(opts.AccountHash) != "" &&
		strings.TrimSpace(opts.CheckpointRunID) != "" &&
		(opts.CheckpointRows > 0 || opts.CheckpointEvery > 0)
	cp := &gmailBackupCheckpointer{
		enabled: enabled,
		opts:    opts,
		total:   total,
		last:    time.Now(),
	}
	if enabled {
		gmailBackupProgressf(ctx, "backup gmail checkpoint\trun=%s\trows=%d\tinterval=%s", opts.CheckpointRunID, opts.CheckpointRows, opts.CheckpointEvery)
	}
	return cp
}

func (c *gmailBackupCheckpointer) record(ctx context.Context, messageID string, done, fetched, cacheHits int) error {
	if c == nil || !c.enabled || strings.TrimSpace(messageID) == "" {
		return nil
	}
	c.pending = append(c.pending, messageID)
	if c.shouldFlush(done) {
		return c.flush(ctx, done, fetched, cacheHits)
	}
	return nil
}

func (c *gmailBackupCheckpointer) shouldFlush(done int) bool {
	if len(c.pending) == 0 {
		return false
	}
	if c.opts.CheckpointRows > 0 && len(c.pending) >= c.opts.CheckpointRows {
		return true
	}
	if c.opts.CheckpointEvery > 0 && time.Since(c.last) >= c.opts.CheckpointEvery {
		return true
	}
	return done == c.total
}

func (c *gmailBackupCheckpointer) flush(ctx context.Context, done, fetched, cacheHits int) error {
	if c == nil || !c.enabled || len(c.pending) == 0 {
		return nil
	}
	c.part++
	ids := append([]string(nil), c.pending...)
	c.pending = c.pending[:0]
	shards, err := gmailbackup.BuildCheckpointShards(ctx, c.opts.Cache, ids, gmailbackup.CheckpointShardOptions{
		AccountHash: c.opts.AccountHash,
		RunID:       c.opts.CheckpointRunID,
		FirstPart:   c.part,
	})
	if err != nil {
		return err
	}
	c.part += len(shards) - 1
	snapshot := backup.Snapshot{
		Services: []string{backupServiceGmail},
		Accounts: []string{c.opts.AccountHash},
		Counts:   map[string]int{"gmail.messages": len(ids)},
		Shards:   shards,
	}
	result, err := backup.PushCheckpoint(ctx, snapshot, backup.Checkpoint{
		RunID:     c.opts.CheckpointRunID,
		Service:   backupServiceGmail,
		Account:   c.opts.AccountHash,
		Done:      done,
		Total:     c.total,
		Fetched:   fetched,
		CacheHits: cacheHits,
	}, c.opts.BackupOptions)
	if err != nil {
		return err
	}
	c.last = time.Now()
	gmailBackupProgressf(ctx, "backup gmail checkpoint\t%d/%d\tparts=%d\trows=%d\tchanged=%t", done, c.total, len(shards), len(ids), result.Changed)
	return nil
}

func listGmailBackupMessageIDs(ctx context.Context, svc *gmail.Service, opts gmailBackupOptions) ([]string, error) {
	var ids []string
	pageToken := ""
	selection := gmailBackupSelection(opts)
	if opts.CacheMessages && !opts.RefreshCache {
		state, ok, err := opts.Cache.ReadListState(selection)
		if err != nil {
			return nil, err
		}
		if ok {
			if state.Complete {
				gmailBackupProgressf(ctx, "backup gmail list\tresume=complete\tmessages=%d", len(state.IDs))
				return append([]string(nil), state.IDs...), nil
			}
			ids = append(ids, state.IDs...)
			pageToken = state.PageToken
			gmailBackupProgressf(ctx, "backup gmail list\tresume=partial\tmessages=%d", len(ids))
		}
	}
	gmailBackupProgressf(ctx, "backup gmail list\tstart\tmessages=%d", len(ids))
	for {
		maxResults := int64(500)
		if opts.Max > 0 {
			remaining := opts.Max - int64(len(ids))
			if remaining <= 0 {
				break
			}
			if remaining < maxResults {
				maxResults = remaining
			}
		}
		call := svc.Users.Messages.List("me").
			MaxResults(maxResults).
			IncludeSpamTrash(opts.IncludeSpamTrash).
			Fields("messages(id),nextPageToken").
			Context(ctx)
		if strings.TrimSpace(opts.Query) != "" {
			call = call.Q(opts.Query)
		}
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return nil, err
		}
		for _, message := range resp.Messages {
			if message != nil && strings.TrimSpace(message.Id) != "" {
				ids = append(ids, message.Id)
			}
		}
		gmailBackupProgressf(ctx, "backup gmail list\tmessages=%d", len(ids))
		complete := resp.NextPageToken == "" || (opts.Max > 0 && int64(len(ids)) >= opts.Max)
		if complete {
			if opts.CacheMessages {
				if err := opts.Cache.WriteListState(selection, ids, "", true); err != nil {
					return nil, err
				}
			}
			break
		}
		pageToken = resp.NextPageToken
		if opts.CacheMessages {
			if err := opts.Cache.WriteListState(selection, ids, pageToken, false); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

func gmailBackupSelection(opts gmailBackupOptions) gmailbackup.Selection {
	return gmailbackup.Selection{
		AccountHash:      opts.AccountHash,
		Query:            opts.Query,
		Max:              opts.Max,
		IncludeSpamTrash: opts.IncludeSpamTrash,
	}
}

func gmailBackupProgressf(ctx context.Context, format string, args ...any) {
	u := ui.FromContext(ctx)
	if u == nil {
		return
	}
	u.Err().Linef(format, args...)
}

func buildGmailMessageShardsFromCheckpoint(ctx context.Context, opts gmailBackupOptions, ids []string) ([]backup.PlainShard, bool, error) {
	if !opts.CacheMessages || !opts.Checkpoints || strings.TrimSpace(opts.AccountHash) == "" || strings.TrimSpace(opts.CheckpointRunID) == "" {
		return nil, false, nil
	}
	cfg, err := backup.ResolveOptions(opts.BackupOptions)
	if err != nil {
		return nil, false, err
	}
	if len(cfg.Recipients) == 0 {
		recipient, recipientErr := backup.RecipientFromIdentity(cfg.Identity)
		if recipientErr != nil {
			return nil, false, recipientErr
		}
		cfg.Recipients = []string{recipient}
	}
	manifest, err := backup.ReadCheckpointManifest(cfg.Repo, gmailBackupCheckpointManifestRel(opts.AccountHash, opts.CheckpointRunID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !gmailBackupCheckpointCompleteForSelection(manifest, opts, ids) {
		return nil, false, nil
	}
	if !sameBackupRecipients(manifest.Recipients, cfg.Recipients) {
		gmailBackupProgressf(ctx, "backup gmail checkpoint-promote\tskip=recipients-changed\trun=%s", opts.CheckpointRunID)
		return nil, false, nil
	}
	shards := make([]backup.PlainShard, 0, len(manifest.Shards))
	rows := 0
	for _, entry := range manifest.Shards {
		if entry.Service != backupServiceGmail || entry.Kind != gmailBackupShardKindMessages || entry.Account != opts.AccountHash {
			return nil, false, fmt.Errorf("gmail checkpoint %s contains unexpected shard %s/%s/%s", opts.CheckpointRunID, entry.Service, entry.Kind, entry.Account)
		}
		shards = append(shards, backup.ExistingShard(entry, manifest.Recipients))
		rows += entry.Rows
	}
	if rows != len(ids) {
		return nil, false, fmt.Errorf("gmail checkpoint %s row count = %d, want %d", opts.CheckpointRunID, rows, len(ids))
	}
	gmailBackupProgressf(ctx, "backup gmail checkpoint-promote\trun=%s\tshards=%d\tmessages=%d", opts.CheckpointRunID, len(shards), rows)
	return shards, true, nil
}

func gmailBackupCheckpointRunID(opts gmailBackupOptions, ids []string) string {
	return time.Now().UTC().Format("20060102T150405Z") + "-" + gmailBackupCheckpointRunIDSuffix(opts, ids)
}

func gmailBackupCheckpointRunIDSuffix(opts gmailBackupOptions, ids []string) string {
	key := struct {
		AccountHash      string `json:"accountHash"`
		Query            string `json:"query,omitempty"`
		Max              int64  `json:"max,omitempty"`
		IncludeSpamTrash bool   `json:"includeSpamTrash"`
		IDs              int    `json:"ids"`
	}{
		AccountHash:      opts.AccountHash,
		Query:            strings.TrimSpace(opts.Query),
		Max:              opts.Max,
		IncludeSpamTrash: opts.IncludeSpamTrash,
		IDs:              len(ids),
	}
	data, _ := json.Marshal(key)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:6])
}

func gmailBackupResolvedCheckpointRunID(ctx context.Context, opts gmailBackupOptions, ids []string) string {
	generated := gmailBackupCheckpointRunID(opts, ids)
	if !opts.Checkpoints || !opts.CacheMessages || strings.TrimSpace(opts.AccountHash) == "" {
		return generated
	}
	suffix := gmailBackupCheckpointRunIDSuffix(opts, ids)
	cfg, err := backup.ResolveOptions(opts.BackupOptions)
	if err != nil {
		return generated
	}
	root := filepath.Join(cfg.Repo, "checkpoints", "gmail", opts.AccountHash)
	entries, err := os.ReadDir(root)
	if err != nil {
		return generated
	}
	runIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), "-"+suffix) {
			runIDs = append(runIDs, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(runIDs)))
	for _, runID := range runIDs {
		manifest, err := backup.ReadCheckpointManifest(cfg.Repo, gmailBackupCheckpointManifestRel(opts.AccountHash, runID))
		if err != nil {
			continue
		}
		if !gmailBackupCheckpointMatchesSelection(manifest, opts, ids) {
			continue
		}
		gmailBackupProgressf(ctx, "backup gmail checkpoint\treuse=%s\tdone=%d/%d", runID, manifest.Done, manifest.Total)
		return runID
	}
	return generated
}

func gmailBackupCheckpointManifestRel(accountHash, runID string) string {
	return fmt.Sprintf("checkpoints/gmail/%s/%s/manifest.json", accountHash, runID)
}

func gmailBackupCheckpointMatchesSelection(manifest backup.CheckpointManifest, opts gmailBackupOptions, ids []string) bool {
	return manifest.Service == backupServiceGmail &&
		manifest.Account == opts.AccountHash &&
		manifest.Total == len(ids) &&
		strings.TrimSpace(manifest.RunID) != ""
}

func gmailBackupCheckpointCompleteForSelection(manifest backup.CheckpointManifest, opts gmailBackupOptions, ids []string) bool {
	return gmailBackupCheckpointMatchesSelection(manifest, opts, ids) &&
		manifest.Done == len(ids) &&
		manifest.Total == len(ids)
}

func sameBackupRecipients(a, b []string) bool {
	a = normalizedBackupStrings(a)
	b = normalizedBackupStrings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func normalizedBackupStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

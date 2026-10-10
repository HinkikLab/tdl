package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flytam/filenamify"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/internal/transfer"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
)

type LinkedOptions struct {
	Chat, Dir, Tag, TagMatch string
	Tags                     []string
	StartID, EndID, MaxPosts int
	Window                   ArchiveWindow
	Unavailable              *UnavailableLinks
	CheckOnly, Takeout       bool
	Threads, Limit           int
	Pool                     dcpool.Pool
	Manager                  *peers.Manager
	Delay                    time.Duration
	Reservations             *transfer.Reservations
	Account                  string
	AccountVerified          bool
	delay                    *transfer.Delay
	Links                    LinkOptions
	Include, Exclude         []string
	BotUpdates               *BotUpdates
	WriteMetadata            *bool // nil enables meta.json output
	OnResolved               func(string)
	OnResult                 func(transfer.Counts)
	counts                   *transfer.Counts
}

type archivedResource struct {
	Identity  string `json:"identity"`
	ChatID    int64  `json:"chat_id"`
	MessageID int    `json:"message_id"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
	DC        int    `json:"dc"`
	Text      string `json:"text,omitempty"`
}

type linkedPost struct {
	tagPost
	Version   int                `json:"version"`
	LinkHash  string             `json:"link_hash"`
	Links     []string           `json:"links"`
	Hops      []linkHop          `json:"hops"`
	Resources []archivedResource `json:"resources"`
	Complete  bool               `json:"complete"`
}

// DownloadLinked processes one source post at a time. Ephemeral bot messages
// are requested just before download, rather than exported for a later pass.
func DownloadLinked(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts LinkedOptions) (rerr error) {
	account, err := archiveAccount(ctx, c, opts.Account, opts.AccountVerified)
	if err != nil {
		return err
	}
	opts.Account = account
	return downloadLinked(ctx, c.API(), kvd, opts)
}

func downloadLinked(ctx context.Context, api *tg.Client, kvd storage.Storage, opts LinkedOptions) (rerr error) {
	opts.counts = &transfer.Counts{}
	defer func() {
		if opts.OnResult != nil {
			opts.OnResult(*opts.counts)
		}
	}()
	if err := opts.Links.Normalize(); err != nil {
		return err
	}
	if opts.Pool == nil {
		return diagnostic.Describe(fmt.Errorf("linked archive requires a download pool"), corei18n.Message{ID: "errors.message.linked_archive_requires_a_download_pool"})
	}
	source, err := parseResourceLink(opts.Chat)
	if err != nil {
		return err
	}
	if source.Kind == linkKindBot || source.Comment != 0 {
		return diagnostic.Describe(fmt.Errorf("source must be a main chat or post"), corei18n.Message{ID: "errors.message.source_must_be_a_main_chat_or_post"})
	}
	if opts.Window == (ArchiveWindow{}) {
		opts.Window = ArchiveWindow{StartID: opts.StartID, EndID: opts.EndID}
	}
	if err := opts.Window.Validate(); err != nil {
		return err
	}
	if opts.MaxPosts < 0 {
		return diagnostic.Describe(fmt.Errorf("max_posts must not be negative"), corei18n.Message{ID: "errors.message.max_key_posts_must_not_be_negative"})
	}
	manager := opts.Manager
	if manager == nil {
		manager = peers.Options{Storage: storage.NewPeers(kvd)}.Build(api)
	}
	if opts.Reservations == nil {
		opts.Reservations = &transfer.Reservations{}
	}
	opts.delay = &transfer.Delay{Duration: opts.Delay}
	peer, err := tutil.GetInputPeer(ctx, manager, source.Chat)
	if err != nil {
		return err
	}
	announceTarget(ctx, TargetNameContext(ctx, peer, 0, "", source.ID == 0), opts.OnResolved)
	backend := &telegramLinkBackend{api: api, manager: manager, opts: opts.Links, updates: opts.BotUpdates}
	defer func() { rerr = multierr.Append(rerr, cleanupLinked(ctx, backend)) }()
	if opts.Unavailable == nil {
		opts.Unavailable = &UnavailableLinks{}
	}
	resolver := &linkResolver{backend: backend, opts: opts.Links, unavailable: opts.Unavailable}
	var tags []string
	if opts.Tag != "" || len(opts.Tags) > 0 {
		tags, err = normalizeTags(opts.Tag, opts.Tags)
		if err != nil {
			return err
		}
	}
	mode, err := ParseTagMatch(opts.TagMatch)
	if err != nil {
		return err
	}
	root := filepath.Join(opts.Dir, strconv.FormatInt(peer.ID(), 10))
	selected, skipped := 0, 0
	var failures error
	process := func(album []*tg.Message) error {
		if len(album) == 0 {
			return nil
		}
		sort.Slice(album, func(i, j int) bool { return album[i].ID < album[j].ID })
		if !opts.Window.matches(album) {
			return nil
		}
		opts.counts.Messages += int64(len(album))
		post, ok := linkedSourcePost(album, tags, mode, peer.ID(), source.Chat)
		if !ok {
			return nil
		}
		post.Source, post.Account = archiveSource(archiveInputPeer(peer)), opts.Account
		opts.counts.Posts++
		var links []resourceLink
		for _, m := range album {
			links = append(links, messageResourceLinks(m)...)
		}
		if len(links) == 0 && defaultOn(opts.Links.ScanComments) {
			for _, m := range album {
				comments, err := backend.Comments(ctx, archiveInputPeer(peer), m, opts.Links.CommentLimit)
				if err != nil {
					return diagnostic.Describe(fmt.Errorf("post %d comments: %w", post.MessageID, err), corei18n.Message{ID: "errors.message.post_value_comments_value", Args: map[string]any{"Arg1": post.MessageID, "Arg2": err}})
				}
				for _, comment := range comments {
					links = append(links, messageResourceLinks(comment.Message)...)
				}
			}
		}
		links = uniqueResourceLinks(links)
		if len(links) == 0 {
			skipped++
			opts.counts.PostsNoLinks++
			return nil
		}
		// The resolver skips cached dead targets per branch. A post may also
		// contain healthy links whose files still need to be archived.
		selected++
		fmt.Println(console.Translate(ctx, messages.LinkedPost(post.MessageID, len(links), post.Directory)))
		if opts.CheckOnly {
			return nil
		}
		err := archiveLinkedPost(ctx, root, post, album, archiveInputPeer(peer), links, resolver, opts)
		if isUnavailableResource(err) {
			selected--
			skipped++
			opts.counts.PostsUnavailable++
			fmt.Println(console.Translate(ctx, messages.LinkedPostSkipped(post.MessageID, console.FormatError(err, corei18n.FromContext(ctx)))))
			err = nil
		}
		return multierr.Append(err, cleanupLinked(ctx, backend))
	}
	runPost := func(album []*tg.Message) (bool, error) {
		before := selected
		if err := process(album); err != nil {
			if selected == before {
				selected++
			} // failed candidates also obey max_posts
			failures = multierr.Append(failures, err)
			fmt.Println(console.Translate(ctx, messages.LinkedPostFailed(console.FormatError(err, corei18n.FromContext(ctx)))))
		}
		return opts.MaxPosts == 0 || selected < opts.MaxPosts, ctx.Err()
	}
	complete := true
	if source.ID > 0 {
		album, err := backend.messageAlbum(ctx, archiveInputPeer(peer), source.ID, false)
		if err != nil {
			return err
		}
		var main []*tg.Message
		for _, m := range album {
			main = append(main, m.Message)
		}
		if _, err := runPost(main); err != nil {
			return err
		}
	} else {
		q := query.NewQuery(api).Messages().GetHistory(archiveInputPeer(peer))
		complete, err = scanArchiveHistory(ctx, q, opts.Window, runPost)
		failures = multierr.Append(failures, err)
	}
	if !complete && err == nil && opts.Window.Until > 0 && !opts.CheckOnly {
		failures = multierr.Append(failures, diagnostic.Describe(fmt.Errorf("max_posts truncated the incremental archive window; keeping last_ts"), corei18n.Message{ID: "errors.message.max_key_posts_truncated_the_incremental_archive_window_keeping_last_key_ts"}))
	}
	fmt.Println(console.Translate(ctx, messages.LinkedArchiveSummary(selected, skipped)))
	return failures
}

func cleanupLinked(ctx context.Context, backend *telegramLinkBackend) error {
	// Cancellation should still clean up the IDs already collected. Keep this
	// bounded so an unavailable Telegram connection cannot hang shutdown.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := backend.Cleanup(cleanupCtx); err != nil {
		return diagnostic.Describe(fmt.Errorf("clean up bot messages: %w", err), corei18n.Message{ID: "errors.message.clean_up_bot_messages_value", Args: map[string]any{"Arg1": err}})
	}
	return nil
}

func uniqueResourceLinks(links []resourceLink) []resourceLink {
	seen := map[string]bool{}
	var result []resourceLink
	for _, l := range links {
		if !seen[l.key()] {
			result = append(result, l)
			seen[l.key()] = true
		}
	}
	return result
}

func linkedSourcePost(album []*tg.Message, tags []string, mode string, chatID int64, chat string) (tagPost, bool) {
	var media []tagMedia
	for _, m := range album {
		item := tagMedia{ID: m.ID, Type: "message", Date: m.Date, Text: m.Message, GroupedID: m.GroupedID}
		if md, ok := tmedia.GetMedia(m); ok {
			item.File, item.Size = md.Name, md.Size
		}
		media = append(media, item)
	}
	if len(tags) > 0 {
		pending := make([]tagMedia, len(media))
		for i, m := range media {
			pending[len(media)-1-i] = m
		}
		post, ok := matchAlbum(pending, tags, mode, chatID, chat)
		if ok {
			post.Directory = linkedDirectory(post.Text, post.MessageID, post.MatchedTags[0])
		}
		return post, ok
	}
	caption := ""
	for _, m := range album {
		if m.Message != "" {
			caption = m.Message
			break
		}
	}
	p := tagPost{ChatID: chatID, MessageID: album[0].ID, GroupedID: album[0].GroupedID, Text: caption, Messages: media}
	p.SourceURL = (resourceLink{Kind: linkKindMessage, Chat: chat, ID: p.MessageID}).URL()
	p.Directory = linkedDirectory(caption, p.MessageID, "")
	return p, true
}

func linkedDirectory(text string, id int, tag string) string {
	return postDirectory(telegramURL.ReplaceAllString(text, " "), id, tag)
}

func linkedFingerprint(roots []resourceLink, opts LinkedOptions) string {
	keys := make([]string, 0, len(roots))
	for _, l := range roots {
		keys = append(keys, l.key())
	}
	sort.Strings(keys)
	b, _ := json.Marshal(struct {
		Version                                         int
		Keys                                            []string
		Previews                                        bool
		Include, Exclude                                []string
		Depth, Links, Messages, TopicMessages, Comments int
		ScanComments                                    bool
	}{2, keys, defaultOn(opts.Links.IncludePreviews), normalizedArchiveExtensions(opts.Include), normalizedArchiveExtensions(opts.Exclude), opts.Links.MaxDepth, opts.Links.MaxLinks, opts.Links.MaxBotMessages, opts.Links.MaxTopicMessages, opts.Links.CommentLimit, defaultOn(opts.Links.ScanComments)})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

func normalizedArchiveExtensions(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		seen[strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item)), ".")] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for item := range seen {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func completedLinkedPost(dir string, post tagPost, hash string) *linkedPost {
	b, err := readArchiveMetadata(dir)
	if err != nil {
		return nil
	}
	var saved linkedPost
	if json.Unmarshal(b, &saved) != nil || saved.Version != 1 || !saved.Complete || !sameArchiveOwner(saved.tagPost, post) || saved.LinkHash != hash || len(saved.Resources) == 0 {
		return nil
	}
	for _, file := range saved.Resources {
		if filepath.Base(file.File) != file.File {
			return nil
		}
		stat, err := os.Stat(filepath.Join(dir, file.File))
		if err != nil || !stat.Mode().IsRegular() || stat.Size() != file.Size {
			return nil
		}
	}
	return &saved
}

func resourceFileName(md *tmedia.Media) (string, error) {
	base := filepath.Base(strings.ReplaceAll(md.Name, `\`, "/"))
	name, err := filenamify.Filenamify(base, filenamify.Options{Replacement: "_", MaxLength: len([]rune(base)) + 1})
	if err != nil {
		return "", err
	}
	// Keep both identity and extension intact within common component limits.
	ext := filepath.Ext(name)
	if len(ext) > 16 {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	for len(stem) > 120 {
		r := []rune(stem)
		stem = string(r[:len(r)-1])
	}
	return resourceIdentity(md) + "_" + stem + ext, nil
}

func linkedExtensionAllowed(name string, include, exclude []string) bool {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	contains := func(items []string) bool {
		for _, v := range items {
			if strings.TrimPrefix(strings.ToLower(v), ".") == ext {
				return true
			}
		}
		return false
	}
	return (len(include) == 0 || contains(include)) && !contains(exclude)
}

func archiveLinkedPost(ctx context.Context, root string, post tagPost, album []*tg.Message, peer tg.InputPeerClass, roots []resourceLink, resolver *linkResolver, opts LinkedOptions) error {
	dir := filepath.Join(root, post.Directory)
	if opts.Reservations == nil {
		opts.Reservations = &transfer.Reservations{}
	}
	if err := checkArchiveOwner(dir, post); err != nil {
		return err
	}
	if defaultOn(opts.WriteMetadata) {
		if _, err := opts.Reservations.Reserve(filepath.Join(dir, "meta.json"), fmt.Sprintf("linked-metadata:%d:%d", post.ChatID, post.MessageID)); err != nil {
			return err
		}
	}
	hash := linkedFingerprint(roots, opts)
	if saved := completedLinkedPost(dir, post, hash); saved != nil {
		for _, file := range saved.Resources {
			if _, err := opts.Reservations.Reserve(filepath.Join(dir, file.File), fmt.Sprintf("linked:%d:%d:%s", post.ChatID, post.MessageID, file.Identity)); err != nil {
				return err
			}
		}
		if opts.counts != nil {
			opts.counts.Files += int64(len(saved.Resources))
			opts.counts.FilesExisting += int64(len(saved.Resources))
		}
		if err := writeArchiveMetadata(dir, saved, opts.WriteMetadata); err != nil {
			return err
		}
		fmt.Println(console.Translate(ctx, messages.LinkedArchiveComplete(post.MessageID)))
		return nil
	}
	files, hops, err := resolver.Resolve(ctx, roots)
	if isUnavailableResource(err) {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := migrateTagDirectoryContext(ctx, root, dir, post); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	meta := linkedPost{tagPost: post, Version: 1, LinkHash: hash, Hops: hops}
	for _, l := range roots {
		meta.Links = append(meta.Links, l.URL())
	}
	if err != nil {
		_ = writeArchiveMetadata(dir, meta, opts.WriteMetadata)
		return err
	}
	session := &linkedSession{resolver: resolver, roots: roots, files: files, hops: hops}
	if defaultOn(opts.Links.IncludePreviews) {
		for _, m := range album {
			if md, ok := tmedia.GetMedia(m); ok && md.Size > 0 {
				files = append(files, linkedResource{resourceMessage: resourceMessage{Peer: peer, Message: m}, Media: md})
			}
		}
	}
	var elems []*linkedElem
	seen := map[string]bool{}
	for _, resource := range files {
		md := resource.Media
		key := resourceIdentity(md)
		if seen[key] {
			continue
		}
		seen[key] = true
		if opts.counts != nil {
			opts.counts.Files++
		}
		if !linkedExtensionAllowed(md.Name, opts.Include, opts.Exclude) {
			if opts.counts != nil {
				opts.counts.FilesFiltered++
			}
			continue
		}
		name, err := resourceFileName(md)
		if err != nil {
			return err
		}
		meta.Resources = append(meta.Resources, archivedResource{Identity: key, ChatID: tutil.GetInputPeerID(resource.Peer), MessageID: resource.Message.ID, File: name, Size: md.Size, DC: md.DC, Text: resource.Message.Message})
		path, err := opts.Reservations.Reserve(filepath.Join(dir, name), fmt.Sprintf("linked:%d:%d:%s", post.ChatID, post.MessageID, key))
		if err != nil {
			return err
		}
		stat, err := os.Stat(path)
		if err == nil && stat.Mode().IsRegular() && stat.Size() == md.Size {
			if opts.counts != nil {
				opts.counts.FilesExisting++
			}
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		elems = append(elems, &linkedElem{resource: resource, path: path, session: session, takeout: opts.Takeout})
	}
	if len(meta.Resources) == 0 {
		return diagnostic.Describe(fmt.Errorf("all resource files excluded by extension filters"), corei18n.Message{ID: "errors.message.all_resource_files_excluded_by_extension_filters"})
	}
	if err := writeArchiveMetadata(dir, meta, opts.WriteMetadata); err != nil {
		return err
	}
	if err = runArchiveMedia(ctx, opts.Pool, opts.Threads, opts.Limit, elems, opts.delay, opts.counts); err != nil {
		return err
	}
	meta.Hops = session.hops
	for i := range meta.Resources {
		for _, f := range session.files {
			if resourceIdentity(f.Media) == meta.Resources[i].Identity {
				meta.Resources[i].ChatID = tutil.GetInputPeerID(f.Peer)
				meta.Resources[i].MessageID = f.Message.ID
				meta.Resources[i].Text = f.Message.Message
			}
		}
	}
	meta.Complete = true
	return writeArchiveMetadata(dir, meta, opts.WriteMetadata)
}

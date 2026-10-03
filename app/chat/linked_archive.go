package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flytam/filenamify"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/utils"
)

type LinkedOptions struct {
	Chat, Dir, Tag, TagMatch string
	Tags                     []string
	StartID, EndID, MaxPosts int
	CheckOnly, Takeout       bool
	Threads, Limit           int
	Pool                     dcpool.Pool
	Links                    LinkOptions
	Include, Exclude         []string
	BotUpdates               *BotUpdates
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
	if err := opts.Links.Normalize(); err != nil {
		return err
	}
	if opts.Pool == nil {
		return fmt.Errorf("linked archive requires a download pool")
	}
	source, err := parseResourceLink(opts.Chat)
	if err != nil {
		return err
	}
	if source.Kind == "bot" || source.Comment != 0 {
		return fmt.Errorf("source must be a main chat or post")
	}
	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(c.API())
	peer, err := tutil.GetInputPeer(ctx, manager, source.Chat)
	if err != nil {
		return err
	}
	backend := &telegramLinkBackend{api: c.API(), manager: manager, opts: opts.Links, updates: opts.BotUpdates}
	defer func() { rerr = multierr.Append(rerr, cleanupLinked(ctx, backend)) }()
	resolver := &linkResolver{backend: backend, opts: opts.Links}
	var tags []string
	if opts.Tag != "" || len(opts.Tags) > 0 {
		tags, err = normalizeTags(opts.Tag, opts.Tags)
		if err != nil {
			return err
		}
	}
	mode := opts.TagMatch
	if mode == "" {
		mode = "any"
	}
	if mode != "any" && mode != "all" {
		return fmt.Errorf("tag_match must be any or all")
	}
	root := filepath.Join(opts.Dir, strconv.FormatInt(peer.ID(), 10))
	selected, skipped := 0, 0
	var failures error
	process := func(album []*tg.Message) error {
		if len(album) == 0 {
			return nil
		}
		sort.Slice(album, func(i, j int) bool { return album[i].ID < album[j].ID })
		if opts.StartID > 0 || opts.EndID > 0 {
			inRange := false
			for _, m := range album {
				if (opts.StartID == 0 || m.ID >= opts.StartID) && (opts.EndID == 0 || m.ID < opts.EndID) {
					inRange = true
				}
			}
			if !inRange {
				return nil
			}
		}
		post, ok := linkedSourcePost(album, tags, mode, peer.ID(), source.Chat)
		if !ok {
			return nil
		}
		var links []resourceLink
		for _, m := range album {
			links = append(links, messageResourceLinks(m)...)
		}
		if len(links) == 0 && defaultOn(opts.Links.ScanComments) {
			for _, m := range album {
				comments, err := backend.Comments(ctx, peer.InputPeer(), m, opts.Links.CommentLimit)
				if err != nil {
					return fmt.Errorf("post %d comments: %w", post.MessageID, err)
				}
				for _, comment := range comments {
					links = append(links, messageResourceLinks(comment.Message)...)
				}
			}
		}
		links = uniqueResourceLinks(links)
		if len(links) == 0 {
			skipped++
			return nil
		}
		selected++
		fmt.Printf("Post %d: %d resource link(s) -> %s\n", post.MessageID, len(links), post.Directory)
		if opts.CheckOnly {
			return nil
		}
		err := archiveLinkedPost(ctx, root, post, album, peer.InputPeer(), links, resolver, opts)
		return multierr.Append(err, cleanupLinked(ctx, backend))
	}
	runPost := func(album []*tg.Message) {
		before := selected
		if err := process(album); err != nil {
			if selected == before {
				selected++
			} // failed candidates also obey max_posts
			failures = multierr.Append(failures, err)
			fmt.Printf("Linked post failed: %s\n", err)
		}
	}
	if source.ID > 0 {
		album, err := backend.messageAlbum(ctx, peer.InputPeer(), source.ID, false)
		if err != nil {
			return err
		}
		var main []*tg.Message
		for _, m := range album {
			main = append(main, m.Message)
		}
		runPost(main)
	} else {
		q := query.NewQuery(c.API()).Messages().GetHistory(peer.InputPeer())
		if opts.EndID > 0 {
			q = q.OffsetID(opts.EndID + 10)
		} // include album members at the boundary
		it := messages.NewIterator(q, 100)
		var pending []*tg.Message
		for it.Next(ctx) {
			m, ok := it.Value().Msg.(*tg.Message)
			if !ok {
				continue
			}
			if len(pending) > 0 && (m.GroupedID == 0 || pending[0].GroupedID != m.GroupedID) {
				runPost(pending)
				pending = nil
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if opts.MaxPosts > 0 && selected >= opts.MaxPosts {
					break
				}
				if opts.StartID > 0 && m.ID < opts.StartID {
					break
				}
			}
			pending = append(pending, m)
			if m.GroupedID == 0 {
				runPost(pending)
				pending = nil
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if opts.MaxPosts > 0 && selected >= opts.MaxPosts {
					break
				}
				if opts.StartID > 0 && m.ID < opts.StartID {
					break
				}
			}
		}
		if err := it.Err(); err != nil {
			failures = multierr.Append(failures, err)
		}
		if len(pending) > 0 && (opts.MaxPosts == 0 || selected < opts.MaxPosts) {
			runPost(pending)
		}
	}
	fmt.Printf("Linked archive: %d selected post(s), %d without resource links.\n", selected, skipped)
	return failures
}

func cleanupLinked(ctx context.Context, backend *telegramLinkBackend) error {
	// Cancellation should still clean up the IDs already collected. Keep this
	// bounded so an unavailable Telegram connection cannot hang shutdown.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := backend.Cleanup(cleanupCtx); err != nil {
		return fmt.Errorf("clean up bot messages: %w", err)
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
	p.SourceURL = (resourceLink{Kind: "message", Chat: chat, ID: p.MessageID}).URL()
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
		Keys             []string
		Previews         bool
		Include, Exclude []string
	}{keys, defaultOn(opts.Links.IncludePreviews), opts.Include, opts.Exclude})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

func completedLinkedPost(dir string, post tagPost, hash string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "message.json"))
	if err != nil {
		return false
	}
	var saved linkedPost
	if json.Unmarshal(b, &saved) != nil || saved.Version != 1 || !saved.Complete || saved.ChatID != post.ChatID || saved.MessageID != post.MessageID || saved.LinkHash != hash || len(saved.Resources) == 0 {
		return false
	}
	for _, file := range saved.Resources {
		if filepath.Base(file.File) != file.File {
			return false
		}
		stat, err := os.Stat(filepath.Join(dir, file.File))
		if err != nil || !stat.Mode().IsRegular() || stat.Size() != file.Size {
			return false
		}
	}
	return true
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

func saveLinkedJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".linked-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, writeErr := f.Write(append(b, '\n'))
	closeErr := f.Close()
	if err := multierr.Combine(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func archiveLinkedPost(ctx context.Context, root string, post tagPost, album []*tg.Message, peer tg.InputPeerClass, roots []resourceLink, resolver *linkResolver, opts LinkedOptions) error {
	dir := filepath.Join(root, post.Directory)
	hash := linkedFingerprint(roots, opts)
	if completedLinkedPost(dir, post, hash) {
		fmt.Printf("Post %d: archive already complete\n", post.MessageID)
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := migrateTagDirectory(root, dir, post.MessageID); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "message.txt"), []byte(post.Text), 0o644); err != nil {
		return err
	}
	files, hops, err := resolver.Resolve(ctx, roots)
	meta := linkedPost{tagPost: post, Version: 1, LinkHash: hash, Hops: hops}
	for _, l := range roots {
		meta.Links = append(meta.Links, l.URL())
	}
	if err != nil {
		_ = saveLinkedJSON(filepath.Join(dir, "message.json"), meta)
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
		if seen[key] || !linkedExtensionAllowed(md.Name, opts.Include, opts.Exclude) {
			continue
		}
		seen[key] = true
		name, err := resourceFileName(md)
		if err != nil {
			return err
		}
		meta.Resources = append(meta.Resources, archivedResource{Identity: key, ChatID: tutil.GetInputPeerID(resource.Peer), MessageID: resource.Message.ID, File: name, Size: md.Size, DC: md.DC, Text: resource.Message.Message})
		path := filepath.Join(dir, name)
		stat, err := os.Stat(path)
		if err == nil && stat.Mode().IsRegular() && stat.Size() == md.Size {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		elems = append(elems, &linkedElem{resource: resource, path: path, session: session, takeout: opts.Takeout})
	}
	if len(meta.Resources) == 0 {
		return fmt.Errorf("all resource files excluded by extension filters")
	}
	if err := saveLinkedJSON(filepath.Join(dir, "message.json"), meta); err != nil {
		return err
	}
	w := prog.New(utils.Byte.FormatBinaryBytes)
	progress := &linkedProgress{writer: w, trackers: map[*linkedElem]*pw.Tracker{}}
	renderDone := make(chan struct{})
	go func() { w.Render(); close(renderDone) }()
	iter := &linkedIter{elems: elems}
	err = downloader.New(downloader.Options{Pool: opts.Pool, Threads: opts.Threads, Iter: iter, Progress: progress, SkipParts: true}).Download(ctx, max(1, opts.Limit))
	// Render initializes asynchronously. Stop until it acknowledges completion,
	// including the case where a small download finishes before Render starts.
renderWait:
	for {
		w.Stop()
		select {
		case <-renderDone:
			break renderWait
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = multierr.Combine(err, progress.err); err != nil {
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
	return saveLinkedJSON(filepath.Join(dir, "message.json"), meta)
}

type linkedSession struct {
	mu       sync.Mutex
	resolver *linkResolver
	roots    []resourceLink
	files    []linkedResource
	hops     []linkHop
	requests int
}

func mediaReference(md *tmedia.Media) []byte {
	switch l := md.InputFileLoc.(type) {
	case *tg.InputDocumentFileLocation:
		return l.FileReference
	case *tg.InputPhotoFileLocation:
		return l.FileReference
	}
	return nil
}

func (s *linkedSession) refresh(ctx context.Context, old linkedResource) (*tmedia.Media, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := resourceIdentity(old.Media)
	find := func() *tmedia.Media {
		for _, f := range s.files {
			if resourceIdentity(f.Media) == key && f.Media.Size == old.Media.Size && f.Media.DC == old.Media.DC && !bytes.Equal(mediaReference(old.Media), mediaReference(f.Media)) {
				return f.Media
			}
		}
		return nil
	}
	if fresh := find(); fresh != nil {
		return fresh, nil
	}
	// Try the original message before reissuing the complete link chain.
	if backend, ok := s.resolver.backend.(*telegramLinkBackend); ok {
		if msg, err := getLinkedMessage(ctx, backend.api, old.Peer, old.Message.ID); err == nil {
			if md, ok := tmedia.GetMedia(msg); ok && resourceIdentity(md) == key && md.Size == old.Media.Size && md.DC == old.Media.DC && !bytes.Equal(mediaReference(old.Media), mediaReference(md)) {
				return md, nil
			}
		}
	}
	if s.requests >= *s.resolver.opts.ReRequestLimit {
		return nil, fmt.Errorf("resource rerequest_limit exhausted")
	}
	s.requests++
	fmt.Printf("Source message expired; requesting resource links again (%d/%d)\n", s.requests, *s.resolver.opts.ReRequestLimit)
	files, hops, err := s.resolver.Resolve(ctx, s.roots)
	if err != nil {
		return nil, err
	}
	if len(s.files) > 0 && !sameLinkedResources(s.files, files) {
		return nil, fmt.Errorf("resource set changed after reissuing the chain; rerun to archive the new set")
	}
	s.files, s.hops = files, hops
	if fresh := find(); fresh != nil {
		return fresh, nil
	}
	return nil, fmt.Errorf("reissued resources do not contain the same file with a fresh reference: %s", key)
}

func sameLinkedResources(a, b []linkedResource) bool {
	if len(a) != len(b) {
		return false
	}
	identities := map[string]string{}
	for _, f := range a {
		identities[resourceIdentity(f.Media)] = fmt.Sprintf("%d:%d", f.Media.Size, f.Media.DC)
	}
	for _, f := range b {
		if identities[resourceIdentity(f.Media)] != fmt.Sprintf("%d:%d", f.Media.Size, f.Media.DC) {
			return false
		}
	}
	return true
}

type linkedMediaFile struct{ media *tmedia.Media }

func (f linkedMediaFile) Location() tg.InputFileLocationClass { return f.media.InputFileLoc }
func (f linkedMediaFile) Size() int64                         { return f.media.Size }
func (f linkedMediaFile) DC() int                             { return f.media.DC }

type linkedElem struct {
	resource linkedResource
	path     string
	session  *linkedSession
	file     *os.File
	parts    *downloader.PartsStore
	takeout  bool
}

func (e *linkedElem) File() downloader.File { return linkedMediaFile{e.resource.Media} }
func (e *linkedElem) To() io.WriterAt       { return e.file }
func (e *linkedElem) AsTakeout() bool       { return e.takeout }
func (e *linkedElem) RefreshFile(ctx context.Context, current tg.InputFileLocationClass) (downloader.File, error) {
	old := e.resource
	media := *old.Media
	media.InputFileLoc = current
	old.Media = &media
	md, err := e.session.refresh(ctx, old)
	if err != nil {
		return nil, err
	}
	return linkedMediaFile{md}, nil
}

type linkedIter struct {
	elems   []*linkedElem
	current *linkedElem
	err     error
}

func (it *linkedIter) Next(ctx context.Context) bool {
	if it.err != nil || len(it.elems) == 0 {
		return false
	}
	if it.err = ctx.Err(); it.err != nil {
		return false
	}
	e := it.elems[0]
	it.elems = it.elems[1:]
	e.file, e.parts, it.err = downloader.OpenPartial(e.path+".tmp", e.resource.Media.Size)
	if it.err != nil {
		return false
	}
	it.current = e
	return true
}
func (it *linkedIter) Value() downloader.Elem { return it.current }
func (it *linkedIter) Err() error             { return it.err }

type linkedProgress struct {
	writer   pw.Writer
	mu       sync.Mutex
	trackers map[*linkedElem]*pw.Tracker
	err      error
}

func (p *linkedProgress) OnAdd(elem downloader.Elem) {
	e := elem.(*linkedElem)
	t := prog.AppendTracker(p.writer, utils.Byte.FormatBinaryBytes, filepath.Base(e.path), e.File().Size())
	p.mu.Lock()
	p.trackers[e] = t
	p.mu.Unlock()
}
func (p *linkedProgress) OnDownload(elem downloader.Elem, s downloader.ProgressState) {
	p.mu.Lock()
	t := p.trackers[elem.(*linkedElem)]
	p.mu.Unlock()
	if t != nil {
		t.SetValue(s.Downloaded)
	}
}
func (p *linkedProgress) OnDone(elem downloader.Elem, err error) {
	e := elem.(*linkedElem)
	err = multierr.Combine(err, e.parts.Flush(), e.file.Close())
	if err == nil {
		stat, statErr := os.Stat(e.path + ".tmp")
		if statErr != nil {
			err = statErr
		} else if stat.Size() != e.File().Size() {
			err = fmt.Errorf("downloaded file size mismatch")
		}
	}
	if err == nil {
		err = os.Rename(e.path+".tmp", e.path)
	}
	if err == nil {
		e.parts.Remove()
		ts := time.Unix(int64(e.resource.Message.Date), 0)
		_ = os.Chtimes(e.path, ts, ts)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		p.trackers[e].MarkAsDone()
	} else {
		p.trackers[e].MarkAsErrored()
		p.err = multierr.Append(p.err, err)
	}
}
func (p *linkedProgress) Resume(elem downloader.Elem) (map[int]struct{}, int64, bool) {
	e := elem.(*linkedElem)
	done := e.parts.Done()
	return done, e.File().Size(), len(done) > 0
}
func (p *linkedProgress) PartDone(elem downloader.Elem, index int) {
	elem.(*linkedElem).parts.PartDone(index)
}
func (p *linkedProgress) Reset(elem downloader.Elem) { elem.(*linkedElem).parts.Reset() }

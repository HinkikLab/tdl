package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/internal/transfer"
)

const tagCompletedName = ".tdl-completed.json"

// The private checkpoint intentionally contains no caption or public metadata.
// It is independent of meta.json so disabling visible metadata keeps recovery.
type tagCompletedState struct {
	Version   int                      `json:"version"`
	ChatID    int64                    `json:"chat_id"`
	MessageID int                      `json:"message_id"`
	Source    string                   `json:"source,omitempty"`
	Account   string                   `json:"account,omitempty"`
	Files     map[int]tagCompletedFile `json:"files"`
	dirty     bool
}
type tagCompletedFile struct {
	Identity downloader.FileIdentity `json:"identity"`
	Path     string                  `json:"path"`
	Size     int64                   `json:"size"`
	ModTime  int64                   `json:"mod_time_ns"`
}

func loadTagCompleted(dir string, post tagPost, reservations *transfer.Reservations) (*tagCompletedState, error) {
	state := &tagCompletedState{Version: 1, ChatID: post.ChatID, MessageID: post.MessageID, Source: post.Source, Account: post.Account, Files: make(map[int]tagCompletedFile)}
	path := filepath.Join(dir, tagCompletedName)
	if err := reservations.ReserveState(path, fmt.Sprintf("tag-completed:%s:%s:%d:%d", post.Source, post.Account, post.ChatID, post.MessageID)); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	var saved tagCompletedState
	if err := json.Unmarshal(b, &saved); err != nil || saved.Version != 1 || saved.ChatID == 0 || saved.MessageID == 0 || (post.Source != "" && saved.Source == "") || (post.Account != "" && saved.Account == "") {
		if err := preserveArchiveFile(path, reservations); err != nil {
			return nil, err
		}
		return state, nil
	}
	owner := tagPost{ChatID: saved.ChatID, MessageID: saved.MessageID, Source: saved.Source, Account: saved.Account}
	if !sameArchiveOwner(owner, post) {
		return nil, fmt.Errorf("private archive completion state belongs to another source/account/post; preserved for recovery")
	}
	if saved.Files == nil {
		saved.Files = make(map[int]tagCompletedFile)
	}
	return &saved, nil
}

func (s *tagCompletedState) matches(id int, path string, identity downloader.FileIdentity) bool {
	record, ok := s.Files[id]
	if !ok || record.Identity != identity || record.Size != identity.Size || record.Path != filepath.Base(path) {
		return false
	}
	stat, err := os.Stat(path)
	return err == nil && stat.Mode().IsRegular() && stat.Size() == record.Size && stat.ModTime().UnixNano() == record.ModTime
}
func (s *tagCompletedState) complete(id int, path string, file downloader.File) error {
	identity, err := downloader.FileIdentityOf(file)
	if err != nil {
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() != file.Size() {
		return fmt.Errorf("cannot record incomplete archive file %q", path)
	}
	s.Files[id] = tagCompletedFile{Identity: identity, Path: filepath.Base(path), Size: stat.Size(), ModTime: stat.ModTime().UnixNano()}
	s.dirty = true
	return nil
}
func (s *tagCompletedState) save(dir string) error {
	return writeArchiveJSON(filepath.Join(dir, tagCompletedName), s)
}

func preserveArchiveFile(path string, reservations *transfer.Reservations) error {
	stat, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return fmt.Errorf("output path %q is not a regular file", path)
	}
	for n := 0; ; n++ {
		backup := path + ".unverified"
		if n > 0 {
			backup = fmt.Sprintf("%s.unverified-%d", path, n)
		}
		if _, err := os.Stat(backup); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := reservations.Reserve(backup, "archive-recovery:"+path); err != nil {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return fmt.Errorf("preserve unverified archive file: %w", err)
		}
		fmt.Printf("Preserved unverified archive file: %s\n", backup)
		return nil
	}
}

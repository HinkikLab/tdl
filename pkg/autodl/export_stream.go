package autodl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/multierr"
)

// exportStream writes diagnostic content with bounded buffering. The published
// export is a complete JSON window; a failed or cancelled scan removes its
// unfinished temporary output.
type exportStream struct {
	file    *os.File
	buffer  *bufio.Writer
	encoder *json.Encoder
	path    string
	first   bool
}

func newExportStream(dir string, dialogID int64) (*exportStream, error) {
	destination := filepath.Join(dir, tmpDirName)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(destination, ".tdl-export-*.new")
	if err != nil {
		return nil, err
	}
	buffer := bufio.NewWriterSize(file, 64*1024)
	s := &exportStream{
		file: file, buffer: buffer, encoder: json.NewEncoder(buffer), first: true,
		path: filepath.Join(destination, fmt.Sprintf("tdl-export-%d-%d.json", dialogID, time.Now().UnixNano())),
	}
	if _, err := fmt.Fprintf(buffer, "{\"id\":%d,\"messages\":[\n", dialogID); err != nil {
		s.Abort()
		return nil, err
	}
	return s, nil
}

func (s *exportStream) Add(item exportMessage) error {
	if s == nil {
		return nil
	}
	if !s.first {
		if _, err := s.buffer.WriteString(","); err != nil {
			return err
		}
	}
	s.first = false
	return s.encoder.Encode(item)
}

func (s *exportStream) Finalize() error {
	if s == nil {
		return nil
	}
	_, writeErr := s.buffer.WriteString("]}\n")
	err := multierr.Combine(writeErr, s.buffer.Flush(), s.file.Close())
	if err != nil {
		return err
	}
	return os.Rename(s.file.Name(), s.path)
}

func (s *exportStream) Abort() {
	if s == nil {
		return
	}
	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
}

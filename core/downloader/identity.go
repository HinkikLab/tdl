package downloader

import (
	"fmt"
	"os"

	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// File kinds recorded in FileIdentity.
const (
	FileKindDocument = "document"
	FileKindPhoto    = "photo"
)

// FileIdentity describes the bytes selected for a resumable download. Expiring
// references, access hashes and source message IDs deliberately do not belong
// to the identity: a bot may issue the same file through a new message.
type FileIdentity struct {
	Kind      string `json:"kind"`
	ID        int64  `json:"id"`
	ThumbSize string `json:"thumb_size,omitempty"`
	Size      int64  `json:"size"`
	DC        int    `json:"dc"`
	PartSize  int    `json:"part_size"`
}

// FileIdentityOf extracts the stable identity used by part journals.
func FileIdentityOf(file File) (FileIdentity, error) {
	if file == nil {
		return FileIdentity{}, diagnostic.Describe(fmt.Errorf("file is required"), corei18n.Message{ID: "errors.message.file_is_required"})
	}
	id := FileIdentity{Size: file.Size(), DC: file.DC(), PartSize: MaxPartSize}
	switch loc := file.Location().(type) {
	case *tg.InputDocumentFileLocation:
		if loc != nil {
			id.Kind, id.ID, id.ThumbSize = FileKindDocument, loc.ID, loc.ThumbSize
		}
	case *tg.InputPhotoFileLocation:
		if loc != nil {
			id.Kind, id.ID, id.ThumbSize = FileKindPhoto, loc.ID, loc.ThumbSize
		}
	default:
		return FileIdentity{}, diagnostic.Describe(fmt.Errorf("unsupported resumable file location %T", loc), corei18n.Message{ID: "errors.message.unsupported_resumable_file_location_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", loc)}})
	}
	if !id.valid() {
		return FileIdentity{}, diagnostic.Describe(fmt.Errorf("invalid resumable file identity: %+v", id), corei18n.Message{ID: "errors.message.invalid_resumable_file_identity_value", Args: map[string]any{"Arg1": id}})
	}
	return id, nil
}

func (id FileIdentity) valid() bool {
	return (id.Kind == FileKindDocument || id.Kind == FileKindPhoto) && id.ID != 0 &&
		id.Size >= 0 && id.DC > 0 && id.PartSize == MaxPartSize
}

// OpenPartialFile opens a partial download only after validating its stable
// media identity. Unverified previous data is preserved for recovery.
func OpenPartialFile(path string, file File) (*os.File, *PartsStore, error) {
	id, err := FileIdentityOf(file)
	if err != nil {
		return nil, nil, err
	}
	return OpenPartial(path, file.Size(), id)
}

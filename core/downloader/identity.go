package downloader

import (
	"fmt"
	"os"

	"github.com/gotd/td/tg"
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
		return FileIdentity{}, fmt.Errorf("file is required")
	}
	id := FileIdentity{Size: file.Size(), DC: file.DC(), PartSize: MaxPartSize}
	switch loc := file.Location().(type) {
	case *tg.InputDocumentFileLocation:
		if loc != nil {
			id.Kind, id.ID, id.ThumbSize = "document", loc.ID, loc.ThumbSize
		}
	case *tg.InputPhotoFileLocation:
		if loc != nil {
			id.Kind, id.ID, id.ThumbSize = "photo", loc.ID, loc.ThumbSize
		}
	default:
		return FileIdentity{}, fmt.Errorf("unsupported resumable file location %T", file.Location())
	}
	if !id.valid() {
		return FileIdentity{}, fmt.Errorf("invalid resumable file identity: %+v", id)
	}
	return id, nil
}

func (id FileIdentity) valid() bool {
	return (id.Kind == "document" || id.Kind == "photo") && id.ID != 0 &&
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

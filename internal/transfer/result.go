package transfer

// Counts keeps units explicit. Messages are source messages examined inside
// the selected window; Posts are archive units passing the caption/tag filter.
// Files counts unique selected media before extension filtering. Bytes counts
// complete payload bytes committed in this run (including resumed bytes), not
// network traffic. Unavailable/no-media counters name their own source unit.
type Counts struct {
	Messages            int64 `json:"messages"`
	Posts               int64 `json:"posts"`
	Files               int64 `json:"files"`
	Bytes               int64 `json:"bytes"`
	FilesDownloaded     int64 `json:"files_downloaded"`
	FilesExisting       int64 `json:"files_existing"`
	FilesFiltered       int64 `json:"files_filtered"`
	FilesFailed         int64 `json:"files_failed"`
	MessagesUnavailable int64 `json:"messages_unavailable"`
	MessagesNoMedia     int64 `json:"messages_no_media"`
	PostsUnavailable    int64 `json:"posts_unavailable"`
	PostsNoLinks        int64 `json:"posts_no_links"`
}

func (c *Counts) Add(other Counts) {
	c.Messages += other.Messages
	c.Posts += other.Posts
	c.Files += other.Files
	c.Bytes += other.Bytes
	c.FilesDownloaded += other.FilesDownloaded
	c.FilesExisting += other.FilesExisting
	c.FilesFiltered += other.FilesFiltered
	c.FilesFailed += other.FilesFailed
	c.MessagesUnavailable += other.MessagesUnavailable
	c.MessagesNoMedia += other.MessagesNoMedia
	c.PostsUnavailable += other.PostsUnavailable
	c.PostsNoLinks += other.PostsNoLinks
}

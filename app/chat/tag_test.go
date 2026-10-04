package chat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasTagMatchesWholeUnicodeHashtag(t *testing.T) {
	tag, err := normalizeTag("绝区零")
	require.NoError(t, err)
	for _, content := range []string{"新图 #绝区零 今天更新", "(#绝区零)", "#绝区零\n第二行"} {
		assert.True(t, hasTag(content, tag), content)
	}
	for _, content := range []string{"#绝区零攻略", "文字#绝区零", "#绝区", "#原神"} {
		assert.False(t, hasTag(content, tag), content)
	}
}

func TestMatchAlbumKeepsAllMediaAndOriginalCaption(t *testing.T) {
	// Telegram history arrives newest first; only the middle member has a caption.
	pending := []tagMedia{
		{ID: 103, File: "last.mp4", GroupedID: 77},
		{ID: 102, File: "middle.jpg", Text: "#绝区零\n原始说明", GroupedID: 77},
		{ID: 101, File: "first.jpg", GroupedID: 77},
	}
	post, ok := matchAlbum(pending, []string{"#绝区零", "#原神"}, "any", 12345, "AVMYS")
	require.True(t, ok)
	assert.Equal(t, 101, post.MessageID)
	assert.Equal(t, "#绝区零\n原始说明", post.Text)
	assert.Equal(t, []int{101, 102, 103}, []int{post.Messages[0].ID, post.Messages[1].ID, post.Messages[2].ID})
	assert.Equal(t, "https://t.me/AVMYS/101", post.SourceURL)
	assert.Equal(t, []string{"#绝区零"}, post.MatchedTags)
	privatePost, ok := matchAlbum(pending, []string{"#绝区零"}, "any", 2255983776, "2255983776")
	require.True(t, ok)
	assert.Equal(t, "https://t.me/c/2255983776/101", privatePost.SourceURL)
	_, ok = matchAlbum(pending, []string{"#绝区零", "#原神"}, "all", 12345, "AVMYS")
	assert.False(t, ok)
	_, ok = matchAlbum(pending, []string{"#原神"}, "any", 12345, "AVMYS")
	assert.False(t, ok)
}

func TestPostDirectoryRemovesTagsAndUnsafeCharacters(t *testing.T) {
	name := postDirectory("#绝区零 #摄影\n作品：夏日/海边? *2026*", 42, "#绝区零")
	assert.Equal(t, "绝区零 作品：夏日 海边 2026 [42]", name)
	assert.LessOrEqual(t, len([]rune(postDirectory(strings.Repeat("很长的说明", 30), 42, "#绝区零"))), 69)
	assert.Equal(t, "绝区零 摄影 [42]", postDirectory("#绝区零 #摄影", 42, "#绝区零"))
	assert.Equal(t, "绝区零 [42]", postDirectory("#绝区零", 42, "#绝区零"))
	assert.Equal(t, "tag1 tag2 tag3 [42]", postDirectory("#tag1 #tag2\n#tag3", 42, "#tag2"))
	assert.Equal(t, "tag1 tag2 [42]", postDirectory("#tag1, #tag2", 42, "#tag2"))
}

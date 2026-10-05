package tgref

import "testing"

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		raw                     string
		opts                    Options
		chat                    string
		message, topic, comment int
		bot                     bool
	}{
		{"https://t.me/source", Options{}, "source", 0, 0, 0, false},
		{"t.me/s/source/10", Options{}, "source", 10, 0, 0, false},
		{"https://telegram.me/c/123/", Options{}, "123", 0, 0, 0, false},
		{"https://t.me/c/123/20/21", Options{}, "123", 21, 20, 0, false},
		{"https://t.me/source/10?thread=20", Options{}, "source", 10, 20, 0, false},
		{"https://t.me/source/10?comment=30", Options{}, "source", 10, 0, 30, false},
		{"@source", Options{AllowBare: true}, "source", 0, 0, 0, false},
		{"tg://resolve?domain=source&post=10", Options{AllowTG: true}, "source", 10, 0, 0, false},
		{"tg://privatepost?channel=123&post=10", Options{AllowTG: true}, "123", 10, 0, 0, false},
		{"https://t.me/resource_bot?start=", Options{}, "resource_bot", 0, 0, 0, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			r, err := Parse(tc.raw, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if r.Chat != tc.chat || r.MessageID != tc.message || r.TopicID != tc.topic || r.CommentID != tc.comment || r.Bot != tc.bot {
				t.Fatalf("unexpected reference: %+v", r)
			}
		})
	}
}

func TestRejectMalformedReferences(t *testing.T) {
	for _, raw := range []string{
		"", "source", "ftp://t.me/source/1", "https://evil.test/source/1",
		"https://user@t.me/source/1", "https://t.me:443/source/1",
		"https://t.me:/source/1", "https://t.me/source/1#fragment",
		"https://t.me/", "https://t.me/s/", "https://t.me/c/", "https://t.me/c/no/1",
		"https://t.me/c/0/1", "https://t.me/source//1", "https://t.me/source/no/1",
		"https://t.me/source/0", "https://t.me/source/-1", "https://t.me/source/1/2/3",
		"https://t.me/source/1?comment=", "https://t.me/source/1?comment=0",
		"https://t.me/source?comment=1", "https://t.me/source/1?comment=2&comment=3",
		"https://t.me/source/1?thread=-1", "https://t.me/source/1?thread=1&thread=2",
		"https://t.me/source/10/11?thread=20", "https://t.me/source?thread=%zz",
		"https://t.me/source/1?start=x", "https://t.me/c/123?start=x",
		"tg://join?invite=abc", "tg://privatepost?channel=no&post=1",
		"tg://resolve/extra?domain=source&post=1", "tg://resolve?domain=source&post=0",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse(raw, Options{AllowTG: true}); err == nil {
				t.Fatalf("accepted malformed reference %q", raw)
			}
		})
	}
	if _, err := Parse("tg://resolve?domain=source&post=1", Options{}); err == nil {
		t.Fatal("tg action enabled without capability")
	}
}

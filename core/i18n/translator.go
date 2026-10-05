package i18n

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"text/template"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	i18ntemplate "github.com/nicksnyder/go-i18n/v2/i18n/template"
	"golang.org/x/text/language"
)

//go:embed messages
var messagesFS embed.FS

// Message is a stable text ID with named template parameters. Default is the
// English fallback used when a resource entry is missing.
type Message struct {
	ID          string
	Args        map[string]any
	Default     string
	PluralCount any
}

// NewMessage constructs a description without exposing the translation engine.
func NewMessage(id, english string, args map[string]any) Message {
	return Message{ID: id, Default: english, Args: args}
}

// Translator is immutable after resource initialization and safe to share
// across the command tree and all work derived from its context.
type Translator interface {
	Language() Language
	Translate(Message) string
}

type catalog struct {
	language  Language
	localizer *goi18n.Localizer
}

// CoreResources returns the immutable resources shipped by core.
func CoreResources() fs.FS { return messagesFS }

// NewTranslator loads all JSON resources in the supplied filesystems before it
// returns. The returned translator has no mutable language setting.
func NewTranslator(lang Language, resources ...fs.FS) (Translator, error) {
	if lang != English && lang != Chinese {
		return nil, fmt.Errorf("unsupported canonical language %q", lang)
	}
	bundle := goi18n.NewBundle(language.English)
	bundle.RegisterUnmarshalFunc("json", json.Unmarshal)
	if len(resources) == 0 {
		resources = []fs.FS{messagesFS}
	}
	if err := ValidateResources(resources...); err != nil {
		return nil, err
	}
	for _, resource := range resources {
		if err := loadResources(bundle, resource); err != nil {
			return nil, err
		}
	}
	return &catalog{
		language:  lang,
		localizer: goi18n.NewLocalizer(bundle, string(lang), string(English)),
	}, nil
}

func loadResources(bundle *goi18n.Bundle, resource fs.FS) error {
	return fs.WalkDir(resource, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(path.Ext(file), ".json") {
			return nil
		}
		if _, err := bundle.LoadMessageFileFS(resource, file); err != nil {
			return fmt.Errorf("load translation resource %q: %w", file, err)
		}
		return nil
	})
}

func (c *catalog) Language() Language { return c.language }

func (c *catalog) Translate(message Message) string {
	if message.ID == "" {
		return renderFallback(message)
	}
	config := &goi18n.LocalizeConfig{
		MessageID:      message.ID,
		TemplateData:   message.Args,
		PluralCount:    message.PluralCount,
		TemplateParser: &i18ntemplate.TextParser{Option: "missingkey=error"},
	}
	if message.Default != "" {
		config.DefaultMessage = &goi18n.Message{ID: message.ID, Other: message.Default}
	}
	text, err := c.localizer.Localize(config)
	if err != nil {
		// Localize may return a valid English fallback together with an error
		// about the requested language. Preserve that usable result.
		var missing *goi18n.MessageNotFoundErr
		if text != "" && errors.As(err, &missing) {
			return text
		}
		if message.Default != "" {
			return renderFallback(message)
		}
		return message.ID
	}
	if message.Default != "" && text == message.Default && strings.Contains(text, "{{") {
		return renderFallback(message)
	}
	return text
}

func renderFallback(message Message) string {
	tmpl, err := template.New(message.ID).Option("missingkey=error").Parse(message.Default)
	if err != nil {
		return message.Default
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, message.Args); err != nil {
		return message.Default
	}
	return out.String()
}

type translatorContextKey struct{}

// WithTranslator binds the run's translator to a context.
func WithTranslator(ctx context.Context, translator Translator) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if translator == nil {
		return ctx
	}
	return context.WithValue(ctx, translatorContextKey{}, translator)
}

// FromContext returns the translator bound to ctx, if any.
func FromContext(ctx context.Context) Translator {
	if ctx == nil {
		return nil
	}
	translator, _ := ctx.Value(translatorContextKey{}).(Translator)
	return translator
}

var (
	englishOnce sync.Once
	english     Translator
)

// EnglishTranslator returns a fixed English translator for stable diagnostic
// Error() strings. Application language selection never mutates this value.
func EnglishTranslator() Translator {
	englishOnce.Do(func() {
		var err error
		english, err = NewTranslator(English)
		if err != nil {
			// Embedded catalog defects are caught in CI. Keep diagnostics usable
			// if initialization nevertheless fails, rather than returning nil.
			english = fallbackCatalog{}
		}
	})
	return english
}

type fallbackCatalog struct{}

func (fallbackCatalog) Language() Language { return English }
func (fallbackCatalog) Translate(message Message) string {
	if message.Default == "" {
		return message.ID
	}
	return renderFallback(message)
}

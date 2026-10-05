package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/go-faster/errors"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/util/logutil"
)

const EnvKey = "TDL_EXTENSION"

type Env struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	AppID     int    `json:"app_id"`
	AppHash   string `json:"app_hash"`
	Session   []byte `json:"session"`
	DataDir   string `json:"data_dir"`
	NTP       string `json:"ntp"`
	Proxy     string `json:"proxy"`
	Pool      int64  `json:"pool"`
	Debug     bool   `json:"debug"`
	Language  string `json:"language,omitempty"`
}

type Options struct {
	// UpdateHandler will be passed to telegram.Client Options.
	UpdateHandler telegram.UpdateHandler
	// Middlewares will be passed to telegram.Client Options,
	// and recovery,retry,flood-wait will be used if nil.
	Middlewares []telegram.Middleware
	// Logger will be used as extension logger,
	// and default logger(write to extension data dir) will be used if nil.
	Logger *zap.Logger
	// Resources contains optional extension-owned translation resources. Use
	// the ext.<extension-name>.* message ID namespace for extension messages.
	Resources []fs.FS
}

type Extension struct {
	name       string           // extension name
	client     *telegram.Client // telegram client
	log        *zap.Logger      // logger
	config     *Config          // extension config
	translator corei18n.Translator
}

type Config struct {
	Namespace string // tdl namespace
	DataDir   string // data directory for extension
	Proxy     string // proxy URL
	Pool      int64  // pool size
	Debug     bool   // debug mode enabled
	Language  string // selected tdl display language
}

func (e *Extension) Name() string {
	return e.name
}

func (e *Extension) Client() *telegram.Client {
	return e.client
}

func (e *Extension) Log() *zap.Logger {
	return e.log
}

func (e *Extension) Config() *Config {
	return e.config
}

// Translator returns the immutable translator selected by the parent tdl run.
func (e *Extension) Translator() corei18n.Translator { return e.translator }

type Handler func(ctx context.Context, e *Extension) error

func New(o Options) func(h Handler) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

	ext, client, err := buildExtension(ctx, o)
	assert(err)
	ctx = extensionContext(ctx, ext)

	return func(h Handler) {
		defer cancel()

		err := tclient.RunWithAuth(ctx, client, func(ctx context.Context) error {
			if err := h(ctx, ext); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}

			return nil
		})
		if err != nil {
			fmt.Println(diagnostic.FormatError(err, ext.translator))
			os.Exit(1)
		}
	}
}

func extensionContext(ctx context.Context, ext *Extension) context.Context {
	ctx = logctx.With(ctx, ext.log)
	return corei18n.WithTranslator(ctx, ext.translator)
}

func buildExtension(ctx context.Context, o Options) (*Extension, *telegram.Client, error) {
	envFile := os.Getenv(EnvKey)
	if envFile == "" {
		return nil, nil, diagnostic.Describe(errors.New("please launch extension with `tdl EXTENSION_NAME`"), corei18n.Message{ID: "errors.message.please_launch_extension_with_tdl_extension_key_name"})
	}

	extEnv, err := os.ReadFile(envFile)
	if err != nil {
		return nil, nil, diagnostic.Describe(errors.Wrap(err, "read env file"), corei18n.Message{ID: "errors.context.read_env_file", Args: map[string]any{"Reason": err}})
	}

	env := &Env{}
	if err = json.Unmarshal(extEnv, env); err != nil {
		return nil, nil, diagnostic.Describe(errors.Wrap(err, "unmarshal extension environment"), corei18n.Message{ID: "errors.context.unmarshal_extension_environment", Args: map[string]any{"Reason": err}})
	}

	if o.Logger == nil {
		level := zap.InfoLevel
		if env.Debug {
			level = zap.DebugLevel
		}
		o.Logger = logutil.New(level, filepath.Join(env.DataDir, "log", "latest.log"))
	}

	var lang corei18n.Language
	if strings.TrimSpace(env.Language) == "" || strings.EqualFold(env.Language, "auto") {
		lang, err = corei18n.ResolveLanguage("", os.Getenv, corei18n.SystemLocale)
	} else {
		lang, err = corei18n.NormalizeLanguage(env.Language)
	}
	if err != nil {
		return nil, nil, diagnostic.Describe(err, corei18n.Message{ID: "errors.cli.invalid_language", Args: map[string]any{"Language": env.Language}})
	}
	if err := corei18n.ValidateNamespace("ext."+env.Name+".", o.Resources...); err != nil {
		return nil, nil, diagnostic.Describe(errors.Wrap(err, "load extension translations"), corei18n.Message{ID: "errors.context.load_extension_translations", Args: map[string]any{"Reason": err}})
	}
	translator, err := corei18n.NewTranslator(lang, append([]fs.FS{corei18n.CoreResources()}, o.Resources...)...)
	if err != nil {
		return nil, nil, diagnostic.Describe(errors.Wrap(err, "load extension translations"), corei18n.Message{ID: "errors.context.load_extension_translations", Args: map[string]any{"Reason": err}})
	}

	// save logger to context
	ctx = logctx.With(ctx, o.Logger)
	ctx = corei18n.WithTranslator(ctx, translator)

	if o.Middlewares == nil {
		o.Middlewares = tclient.NewDefaultMiddlewares(ctx, 0)
	}

	client, err := buildClient(ctx, env, o)
	if err != nil {
		return nil, nil, diagnostic.Describe(errors.Wrap(err, "build client"), corei18n.Message{ID: "errors.context.build_client", Args: map[string]any{"Reason": err}})
	}

	return &Extension{
		name:   env.Name,
		client: client,
		log:    o.Logger,
		config: &Config{
			Namespace: env.Namespace,
			DataDir:   env.DataDir,
			Proxy:     env.Proxy,
			Pool:      env.Pool,
			Debug:     env.Debug,
			Language:  string(lang),
		},
		translator: translator,
	}, client, nil
}

func buildClient(ctx context.Context, env *Env, o Options) (*telegram.Client, error) {
	storage := &session.StorageMemory{}
	if err := storage.StoreSession(ctx, env.Session); err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "store session"), corei18n.Message{ID: "errors.context.store_session", Args: map[string]any{"Reason": err}})
	}

	return tclient.New(ctx, tclient.Options{
		AppID:            env.AppID,
		AppHash:          env.AppHash,
		Session:          storage,
		Middlewares:      o.Middlewares,
		Proxy:            env.Proxy,
		NTP:              env.NTP,
		ReconnectTimeout: 0, // no timeout
		UpdateHandler:    o.UpdateHandler,
	})
}

func assert(err error) {
	if err != nil {
		lang, _ := corei18n.ResolveLanguage("", os.Getenv, corei18n.SystemLocale)
		if data, readErr := os.ReadFile(os.Getenv(EnvKey)); readErr == nil {
			var env Env
			if json.Unmarshal(data, &env) == nil {
				if selected, parseErr := corei18n.NormalizeLanguage(env.Language); parseErr == nil {
					lang = selected
				}
			}
		}
		translator, loadErr := corei18n.NewTranslator(lang)
		if loadErr != nil {
			translator = corei18n.EnglishTranslator()
		}
		fmt.Fprintln(os.Stderr, diagnostic.FormatError(err, translator))
		os.Exit(1)
	}
}

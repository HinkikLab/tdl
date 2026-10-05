package up

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/fatih/color"
	"github.com/gabriel-vasile/mimetype"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/spf13/viper"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/uploader"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/messages"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/texpr"
	"github.com/iyear/tdl/pkg/utils"
)

type Options struct {
	Chat     string
	Thread   int
	To       string
	Paths    []string
	Includes []string
	Excludes []string
	Remove   bool
	Photo    bool
	Caption  string
}

type Env struct {
	FilePath  string `comment:"File path" comment_id:"fields.file_path"`
	FileName  string `comment:"File name" comment_id:"fields.file_name"`
	FileExt   string `comment:"File extension" comment_id:"fields.file_extension"`
	ThumbPath string `comment:"Thumbnail path" comment_id:"fields.thumbnail_path"`
	MIME      string `comment:"File mime type" comment_id:"fields.file_mime_type"`
}

func Run(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts Options) (rerr error) {
	if opts.To == "-" || opts.Caption == "-" {
		fg := texpr.NewFieldsGetter(nil)

		fields, err := fg.Walk(exprEnv(context.Background(), nil))
		if err != nil {
			return diagnostic.Describe(fmt.Errorf("failed to walk fields: %w", err), corei18n.Message{ID: "errors.message.failed_to_walk_fields_value", Args: map[string]any{"Arg1": err}})
		}

		fmt.Print(fg.SprintContext(ctx, fields, true))
		return nil
	}

	files, err := walk(opts.Paths, opts.Includes, opts.Excludes)
	if err != nil {
		return err
	}

	color.Blue("%s", console.Translate(ctx, messages.UploadFilesCount(len(files))))

	pool := dcpool.NewPool(c,
		int64(viper.GetInt(consts.FlagPoolSize)),
		tclient.NewDefaultMiddlewares(ctx, viper.GetDuration(consts.FlagReconnectTimeout))...)
	defer multierr.AppendInvoke(&rerr, multierr.Close(pool))

	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(pool.Default(ctx))

	to, err := resolveDest(ctx, manager, opts.To)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "get target peer"), corei18n.Message{ID: "errors.context.get_target_peer", Args: map[string]any{"Reason": err}})
	}

	caption, err := resolveCaption(ctx, opts.Caption)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "get caption"), corei18n.Message{ID: "errors.context.get_caption", Args: map[string]any{"Reason": err}})
	}

	upProgress := prog.NewContext(ctx, utils.Byte.FormatBinaryBytes)
	upProgress.SetNumTrackersExpected(len(files))
	if !viper.GetBool(consts.FlagDisableProgressPS) {
		prog.EnablePS(ctx, upProgress)
	}

	options := uploader.Options{
		Client:   pool.Default(ctx),
		Threads:  viper.GetInt(consts.FlagThreads),
		Iter:     newIter(files, to, caption, opts.Chat, opts.Thread, opts.Photo, opts.Remove, viper.GetDuration(consts.FlagDelay), manager),
		Progress: newProgressContext(ctx, upProgress),
	}

	up := uploader.New(options)

	stopRender := prog.Start(upProgress)
	defer stopRender()

	return up.Upload(ctx, viper.GetInt(consts.FlagLimit))
}

func resolveDest(ctx context.Context, manager *peers.Manager, input string) (*vm.Program, error) {
	compile := func(i string) (*vm.Program, error) {
		return expr.Compile(i, expr.Env(exprEnv(ctx, nil)))
	}

	if input == "" {
		return compile(`""`)
	}

	if exp, err := os.ReadFile(input); err == nil {
		return compile(string(exp))
	}

	if _, err := tutil.GetInputPeer(ctx, manager, input); err == nil {
		return compile(fmt.Sprintf(`"%s"`, input))
	}

	return compile(input)
}

func resolveCaption(ctx context.Context, input string) (*vm.Program, error) {
	compile := func(i string) (*vm.Program, error) {
		// we pass empty peer and message to enable type checking
		return expr.Compile(i, expr.Env(exprEnv(ctx, nil)), expr.AsKind(reflect.String))
	}

	// default
	if input == "" {
		return compile(`""`)
	}

	// file
	if exp, err := os.ReadFile(input); err == nil {
		return compile(string(exp))
	}

	// text
	return compile(input)
}

func exprEnv(ctx context.Context, file *File) Env {
	if file == nil {
		return Env{}
	}

	extension := filepath.Ext(file.File)
	filename := strings.TrimSuffix(filepath.Base(file.File), extension)
	mime, err := mimetype.DetectFile(file.File)
	if err != nil {
		mime = &mimetype.MIME{}
		logctx.From(ctx).Error("detect file mime", zap.Error(err))
	}

	return Env{
		FilePath:  file.File,
		FileName:  filename,
		FileExt:   extension,
		ThumbPath: file.Thumb,
		MIME:      mime.String(),
	}
}

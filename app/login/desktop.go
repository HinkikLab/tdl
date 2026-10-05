package login

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gotd/td/session"
	tdtdesktop "github.com/gotd/td/session/tdesktop"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/pkg/console"
	localizedprompt "github.com/iyear/tdl/pkg/console/prompt"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/key"
	"github.com/iyear/tdl/pkg/kv"
	"github.com/iyear/tdl/pkg/messages"
	"github.com/iyear/tdl/pkg/tclient"
	"github.com/iyear/tdl/pkg/tdesktop"
	"github.com/iyear/tdl/pkg/tpath"
)

const tdata = "tdata"

func Desktop(ctx context.Context, opts Options) error {
	ns := viper.GetString(consts.FlagNamespace)

	kvd, err := kv.From(ctx).Open(ns)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "open kv"), corei18n.Message{ID: "errors.context.open_kv", Args: map[string]any{"Reason": err}})
	}

	desktop, err := findDesktop(opts.Desktop)
	if err != nil {
		return err
	}

	color.Blue("%s", console.Translate(ctx, messages.LoginImportDesktop(desktop)))

	accounts, err := tdtdesktop.Read(appendTData(desktop), []byte(opts.Passcode))
	if err != nil {
		return err
	}

	infos := make([]string, 0, len(accounts))
	infoMap := make(map[string]tdtdesktop.Account)
	for _, acc := range accounts {
		id := strconv.FormatUint(acc.Authorization.UserID, 10)
		infos = append(infos, id)
		infoMap[id] = acc
	}

	fmt.Println()
	sel, acc := &localizedprompt.Select{
		Message: console.Translate(ctx, messages.LoginChooseUserID()),
		Options: infos,
		Help:    console.Translate(ctx, messages.LoginUserIDHelp()),
	}, ""
	if err = localizedprompt.AskOne(ctx, sel, &acc); err != nil {
		return err
	}

	data, err := session.TDesktopSession(infoMap[acc])
	if err != nil {
		return err
	}

	loader := &session.Loader{Storage: storage.NewSession(kvd, true)}
	if err = loader.Save(ctx, data); err != nil {
		return err
	}

	if err = kvd.Set(ctx, key.App(), []byte(tclient.AppDesktop)); err != nil {
		return err
	}

	color.Green("%s", console.Translate(ctx, messages.LoginImported(acc, ns)))

	// logout
	confirm, logout := &localizedprompt.Confirm{
		Message: console.Translate(ctx, messages.LoginLogoutPrompt()),
		Default: false,
		Help:    console.Translate(ctx, messages.LoginLogoutHelp()),
	}, false
	if err = localizedprompt.AskOne(ctx, confirm, &logout); err != nil {
		return err
	}

	if logout {
		if err = forceLogout(infoMap[acc].IDx, desktop); err != nil {
			return err
		}
		color.Green("%s", console.Translate(ctx, messages.LoginLoggedOut(infoMap[acc].Authorization.UserID)))
	}

	return nil
}

func findDesktop(desktop string) (string, error) {
	if desktop == "" { // auto detect
		if desktop = detectAppData(); desktop == "" {
			return "", diagnostic.Describe(fmt.Errorf("no data found in possible paths, please specify path to Telegram Desktop directory with `-d` flag"), corei18n.Message{ID: "errors.login.desktop_data_missing"})
		}
		return desktop, nil
	}

	// specified path
	stat, err := os.Stat(desktop)
	if err != nil {
		return "", err
	}
	if !stat.IsDir() { // process path that points to Telegram executable file
		desktop = filepath.Dir(desktop)
	}

	return desktop, nil
}

func detectAppData() string {
	for _, p := range tpath.Desktop.AppData(consts.HomeDir) {
		if path := appendTData(p); fsutil.PathExists(path) {
			return path
		}
	}

	return ""
}

func appendTData(path string) string {
	if filepath.Base(path) != tdata {
		path = filepath.Join(path, tdata)
	}

	return path
}

// forceLogout currently only remove session file
func forceLogout(idx uint32, desktop string) error {
	dir := "data"
	if idx > 0 {
		dir = fmt.Sprintf("data#%d", idx+1)
	}

	return os.RemoveAll(filepath.Join(appendTData(desktop), tdesktop.FileKey(dir)))
}

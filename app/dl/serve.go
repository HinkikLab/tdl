package dl

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gorilla/mux"
	"github.com/gotd/contrib/http_io"
	"github.com/gotd/contrib/partio"
	"github.com/gotd/contrib/tg_io"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/tmessage"
)

type media struct {
	*tmedia.Media
	MIME string
}

type mediaKey struct {
	peer    string
	message int
}

//go:embed serve.go.tmpl
var tmpl string

func serve(ctx context.Context,
	kvd storage.Storage,
	pool dcpool.Pool,
	dialogs [][]*tmessage.Dialog,
	port int,
	takeout bool,
) error {
	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(pool.Default(ctx))

	router := mux.NewRouter()

	cache := &sync.Map{} // map[mediaKey]*media
	router.Handle("/{peer}/{message:[0-9]+}", handler(func(w http.ResponseWriter, r *http.Request) error {
		requestCtx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stopCancel := context.AfterFunc(ctx, cancel)
		defer stopCancel()
		vars := mux.Vars(r)
		peer := vars["peer"]
		messageStr := vars["message"]
		message, err := strconv.Atoi(messageStr)
		if err != nil {
			return errors.Wrap(err, "invalid message id")
		}
		key := mediaKey{peer: peer, message: message}

		var item *media
		if t, ok := cache.Load(key); ok {
			item = t.(*media)
		} else {
			p, err := tutil.GetInputPeer(requestCtx, manager, peer)
			if err != nil {
				return errors.Wrap(err, "resolve peer")
			}

			msg, err := tutil.GetSingleMessage(requestCtx, pool.Default(requestCtx), p.InputPeer(), message)
			if err != nil {
				return errors.Wrap(err, "resolve message")
			}

			item, err = convItem(msg)
			if err != nil {
				return errors.Wrap(err, "convItem")
			}

			cache.Store(key, item)
		}

		api := pool.Client(requestCtx, item.DC)
		if takeout {
			api = pool.Takeout(requestCtx, item.DC)
		}

		u := partio.NewStreamer(
			tg_io.NewDownloader(api).ChunkSource(item.Size, item.InputFileLoc),
			int64(viper.GetInt(consts.FlagPartSize)))

		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Name))

		http_io.NewHandler(u, item.Size).
			WithContentType(item.MIME).
			WithLog(logctx.From(ctx).Named("serve")).
			ServeHTTP(w, r)
		return nil
	}))

	items := make([]string, 0)
	for _, dialog := range dialogs {
		for _, d := range dialog {
			if d == nil {
				continue
			}
			for _, m := range d.Messages {
				items = append(items, fmt.Sprintf("%d/%d", tutil.GetInputPeerID(d.Peer), m))
			}
		}
	}

	list := bytes.NewBuffer(nil)
	err := template.Must(template.New("serve.go.tmpl").Parse(tmpl)).Execute(list, items)
	if err != nil {
		return errors.Wrap(err, "execute template")
	}

	router.Handle("/", handler(func(w http.ResponseWriter, r *http.Request) error {
		_, err := fmt.Fprint(w, list.String())
		return err
	}))

	s := http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
	}

	serverDone := make(chan struct{})
	defer close(serverDone)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = s.Shutdown(shutdownCtx)
		case <-serverDone:
		}
	}()

	color.Green("(Beta) Serving on http://localhost:%d", port)

	err = s.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}

func handler(h func(w http.ResponseWriter, r *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	})
}

func convItem(msg *tg.Message) (*media, error) {
	md, ok := tmedia.GetMedia(msg)
	if !ok {
		return nil, errors.New("message is not a media")
	}

	mime := ""
	switch m := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.AsNotEmpty()
		if !ok {
			return nil, errors.New("document is empty")
		}
		mime = doc.MimeType
	case *tg.MessageMediaPhoto:
		mime = "image/jpeg"
	}

	return &media{
		Media: md,
		MIME:  mime,
	}, nil
}

package logging

import (
	"github.com/gordy96/fb_parser/pkg/fb/worker/errors"
	"github.com/gordy96/fb_parser/pkg/geo/google"
	"log"
	"os"
	"sync"
	"time"
)

var logMux = sync.Mutex{}
var Errlog *log.Logger = log.New(os.Stderr, "", 0)
var Logger *log.Logger = log.New(os.Stdout, "", 0)

func LogError(e error) {
	logMux.Lock()
	defer logMux.Unlock()
	l := func(err error, req []byte, resp []byte) {
		Errlog.Printf(
			"[%s] ERROR: %s\nREQUEST: %s\nRESPONSE: %s\n____________________________________________________\n\n",
			time.Now().Format(time.RFC3339),
			err,
			req,
			resp,
		)
	}
	switch err := e.(type) {
	case google.CannotParseError:
		l(err, err.Request, err.Response)
	case errors.ParsingError:
		l(err, err.Request, err.Response)
	case errors.NoFriendsError:
		l(err, err.Request, err.Response)
	case errors.NoAlbumsError:
		l(err, err.Request, err.Response)
	case errors.GenderUndefinedError:
		l(err, err.Request, err.Response)
	case errors.WorkerCheckpointError:
		l(err, err.Request, err.Response)
	case errors.BrokenLinkCheckpoint:
		l(err, err.Request, err.Response)
	case errors.AuthenticationFailedError:
		l(err, err.Request, err.Response)
	default:
		Errlog.Printf("[%s] ERROR: %v\n", time.Now().Format(time.RFC3339), err)
	}
}

func LogAnything(v interface{}) {
	logMux.Lock()
	defer logMux.Unlock()
	Logger.Printf("[%s] INFO: %v\n", time.Now().Format(time.RFC3339), v)
}

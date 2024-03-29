package tasks

import (
	defErrors "errors"
	"fmt"
	"github.com/gordy96/fb_parser/pkg/fb/photo"
	"github.com/gordy96/fb_parser/pkg/fb/util"
	"github.com/gordy96/fb_parser/pkg/fb/worker"
	"github.com/gordy96/fb_parser/pkg/fb/worker/errors"
	"github.com/gordy96/fb_parser/pkg/logging"
	"github.com/gordy96/fb_parser/pkg/queue"
	"net/http"
	"os"
	"time"
)

type PhotoFullCommand struct {
	WorkerService 	*worker.AccountService
	PhotoService  	*photo.Service
	Queue			*queue.Queue
}

func (p PhotoFullCommand) Handle() error {
	defer func() {
		Session.DecrementPhotoTasksAwaiting()
		Session.IncrementPhotoTasksDone()
	}()

	ph, _ := p.PhotoService.FindNextToDownload()

	if ph == nil {
		return nil
	}

	start := time.Now()
	if ph.FullLink != "" {
		p.download(ph)
		p.requeue()
		return nil
	}
	for time.Now().Sub(start) < 15*time.Minute {
		w, err := p.WorkerService.FindNextRandom()
		if err != nil {
			logging.LogError(err)
			logging.LogAnything("worker stops see error log")
			ph.Status = photo.Unprocessed
			p.PhotoService.Save(ph)
			return nil
		}

		if w != nil {
			//logAnything(fmt.Sprintf("got worker %s", w.Email))
			w.Init()
			fullLink, err := w.GetPhotoFull(ph.ID)
			if err != nil {
				logging.LogError(err)
				logging.LogAnything(fmt.Sprintf("worker %s got critical exception. See logs", w.Email))

				switch err.(type) {
				case errors.WorkerCheckpointError:
					logging.LogAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
					p.WorkerService.Disable(w)
					continue
				case errors.BrokenLinkCheckpoint:
					logging.LogAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
					p.WorkerService.Disable(w)
					continue
				}
				//logAnything(fmt.Sprintf("releasing worker %s", w.Email))
				p.WorkerService.Release(w)
				ph.Status = photo.Error
				p.PhotoService.Save(ph)
				return nil
			}
			//logAnything(fmt.Sprintf("releasing worker %s", w.Email))
			p.WorkerService.Release(w)
			if fullLink != "" {
				ph.FullLink = fullLink
				p.download(ph)
			} else {
				ph.Status = photo.Unprocessed
				p.PhotoService.Save(ph)
			}
			p.requeue()
			return nil
		}
		sleepMillis(20)
	}
	ph.Status = photo.Unprocessed
	p.PhotoService.Save(ph)
	logging.LogError(defErrors.New(fmt.Sprintf("couldn't acquire worker account saving %s/%s/%s", ph.UserID, ph.AlbumID, ph.ID)))
	return nil
}

func (p *PhotoFullCommand) download(ph *photo.Photo) {
	ph.Status = photo.Processed
	p.PhotoService.Save(ph)
	Session.IncrementPhotosDownloaded()

	go SaveFullPhoto(ph.UserID, ph.AlbumID, ph.ID, ph.FullLink)
}

func (p *PhotoFullCommand) requeue() {
	p.Queue.Enqueue(PhotoFullCommand{
		WorkerService: p.WorkerService,
		PhotoService:  p.PhotoService,
		Queue: p.Queue,
	})
}

func SaveFullPhoto(userId string, albumId string, photoId string, link string) {
	os.MkdirAll(fmt.Sprintf("./storage/%s", userId), 0777)
	var err error
	var resp *http.Response
	resp, err = http.Get(link)
	if err != nil {
		panic(err)
	}
	content := util.ReadAll(resp)
	resp.Body.Close()
	var f *os.File
	fileName := fmt.Sprintf("./storage/%s/%s_%s.jpg", userId, albumId, photoId)
	f, err = os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE, 0777)
	if err != nil {
		for err != nil {
			sleepMillis(100)
			f, err = os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE, 0777)
		}
	}

	f.Write(content)
	f.Close()
}

func sleepMillis(dur int) {
	if dur > 0 {
		time.Sleep(time.Duration(dur) * time.Millisecond)
	}
}
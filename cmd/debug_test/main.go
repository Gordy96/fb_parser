package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gordy96/fb_parser/pkg/fb"
	"github.com/gordy96/fb_parser/pkg/fb/photo"
	"github.com/gordy96/fb_parser/pkg/fb/place"
	"github.com/gordy96/fb_parser/pkg/fb/worker"
	errors2 "github.com/gordy96/fb_parser/pkg/fb/worker/errors"
	"github.com/gordy96/fb_parser/pkg/geo"
	"github.com/gordy96/fb_parser/pkg/geo/google"
	"github.com/gordy96/fb_parser/pkg/logging"
	"github.com/gordy96/fb_parser/pkg/queue"
	"github.com/gordy96/fb_parser/pkg/session"
	"github.com/gordy96/fb_parser/pkg/tasks"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/x/network/connstring"
	"os"
	"strings"
	"time"
)

type CountryService struct {
	col *mongo.Collection
}

func (c CountryService) GetCountryNameFromPoint(p geo.Point) (string, error) {
	o := options.FindOne()
	o.Projection = bson.M{"properties.iso_a3": 1}

	r := c.col.FindOne(nil, bson.M{
		"geometry": bson.M{
			"$nearSphere": bson.M{
				"$geometry": bson.M{
					"type":        "Point",
					"coordinates": []float64{p.X, p.Y},
				},
				"$maxDistance": 1000,
			},
		},
	})
	m := bson.M{}
	err := r.Decode(&m)
	if err != nil {
		return "", err
	}
	if properties, ok := m["properties"]; ok {
		if code, ok := properties.(bson.M)["iso_a3"]; ok {
			if s, ok := code.(string); ok {
				return s, nil
			}
		}
	}
	return "", geo.InTheMiddleOfNoWhereError{Point: p}
}

func CheckSavedPhoto(userId string, albumId string, photoId string) bool {
	f, err := os.Open(fmt.Sprintf("./storage/%s/%s_%s.jpg", userId, albumId, photoId))
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func sleepMillis(dur int) {
	if dur > 0 {
		time.Sleep(time.Duration(dur) * time.Millisecond)
	}
}

func recursiveSearch(as *fb.AccountService, account *fb.Account, ws *worker.AccountService, worker *worker.FBAccount, depth int, maxPhotos int, minPhotos int) error {
	if account.ID == "" {
		if account.Nickname == "" {
			return errors.New("malformed account")
		}
		id, err := worker.GetIDFromNickname(account.Nickname)
		if err != nil {
			return err
		}
		account.ID = id
	}
	err := worker.GetUserInfo(account)
	if err != nil {
		switch e := err.(type) {
		case errors2.GenderUndefinedError:
			logging.LogError(e)
			//as.Delete(&account)
		default:
			return err
		}
	}
	as.Save(account)
	session.Default().IncrementAccountsAdded()
	sleepMillis(worker.RequestTimeout)
	foundPhotos := 0
	if !account.PhotosParsed {
		albums, err := worker.GetUserAlbums(account)
		if err != nil {
			switch e := err.(type) {
			case errors2.NoAlbumsError:
				logging.LogError(e)
			default:
				return err
			}
		}
		sleepMillis(worker.RequestTimeout)
		if len(albums) > 0 {
			for _, album := range albums {
				var i = 0
				photos, more, err := worker.GetAlbumPhotosIDS(*account, album, i*12)
				if err != nil {
					return err
				}
				i++
				var temp []string
				sleepMillis(worker.RequestTimeout)
				for more && len(photos) < maxPhotos {
					temp, more, err = worker.GetAlbumPhotosIDS(*account, album, i*12)
					if err != nil {
						return err
					}
					photos = append(photos, temp...)
					i++
					sleepMillis(worker.RequestTimeout)
				}
				if len(photos) >= minPhotos {
					for _, id := range photos {
						foundPhotos++
						if !CheckSavedPhoto(account.ID, album, id) {
							enqueuePhotoFull(ws, photoService, photo.Photo{ID: id, AlbumID: album, UserID: account.ID})
						}
					}
				}
			}
			account.PhotosParsed = true
		}
	}

	foundFriends := 0
	if !account.FriendsParsed {
		if depth > 0 {
			friends, cursor, err := worker.GetUserFriendsList(account, "")
			if err != nil {
				switch e := err.(type) {
				case errors2.NoFriendsError:
					logging.LogError(e)
					return nil
				default:
					return err
				}
			}
			sleepMillis(worker.RequestTimeout)
			var temp []fb.Account
			for cursor != "" {
				temp, cursor, err = worker.GetUserFriendsList(account, cursor)
				if err != nil {
					return err
				}
				friends = append(friends, temp...)
				sleepMillis(worker.RequestTimeout)
			}
			if friends != nil {
				account.Friends = func() []string {
					s := make([]string, len(friends))
					for i, f := range friends {
						if f.ID != "" {
							s[i] = f.ID
						} else {
							s[i] = f.Nickname
						}
					}
					return s
				}()

				foundFriends = len(friends)

				go func(list []fb.Account) {
					for _, friend := range list {
						enqueueCrawl(as, friend, ws, depth-1, maxPhotos, minPhotos)
					}
				}(friends)
				account.FriendsParsed = true
			}
		}
	}
	logging.LogAnything(fmt.Sprintf("%s[%s]: %d photos, %d freinds", account.Nickname, account.ID, foundPhotos, foundFriends))
	return nil
}


type RecursCommand struct {
	WorkerService  *worker.AccountService
	AccountService *fb.AccountService
	Depth          int
	MaxPhotos      int
	MinPhotos      int
}

func (r RecursCommand) Handle() error {
	start := time.Now()

	defer func() {
		session.Default().DecrementAccountTasksAwaiting()
		session.Default().IncrementAccountTasksDone()
	}()

	acc, _ := r.AccountService.Find(currentTestID)
	if acc == nil {
		logging.LogAnything(fmt.Sprintf("no account to process"))
		return nil
	}

	for time.Now().Sub(start) < 15*time.Minute {
		w, err := r.WorkerService.FindNextRandom()
		if err != nil {
			logging.LogError(err)
			logging.LogAnything("worker stops see error log")
			acc.Status = fb.Unprocessed
			r.AccountService.Save(acc)
			return err
		}

		if w != nil {
			//logAnything(fmt.Sprintf("got worker %s", w.Email))
			w.Init()
			err = recursiveSearch(r.AccountService, acc, r.WorkerService, w, r.Depth, r.MaxPhotos, r.MinPhotos)

			sleepMillis(w.ReleaseTimeout)
			if err != nil {
				logging.LogError(err)
				logging.LogAnything(fmt.Sprintf("worker %s got critical exception. See logs", w.Email))
				switch err.(type) {
				case errors2.WorkerCheckpointError:
					logging.LogAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
					r.WorkerService.Disable(w)
					continue
				case errors2.BrokenLinkCheckpoint:
					logging.LogAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
					r.WorkerService.Disable(w)
					continue
				}
				//logAnything(fmt.Sprintf("releasing worker %s", w.Email))
				r.WorkerService.Release(w)

				acc.Status = fb.Unprocessed
				r.AccountService.Save(acc)
				return nil
			}
			//logAnything(fmt.Sprintf("releasing worker %s", w.Email))
			r.WorkerService.Release(w)

			acc.Status = fb.Processed
			r.AccountService.Save(acc)
			return nil
		}
		sleepMillis(20)
	}
	acc.Status = fb.Unprocessed
	r.AccountService.Save(acc)
	return nil
}



var taskQueue *queue.Queue
var photoQueue *queue.Queue

func enqueuePhotoFull(ws *worker.AccountService, ps *photo.Service, p photo.Photo) {
	ps.Save(&p)
	enqueueFullNoPhoto(ws, ps)
}

func enqueueFullNoPhoto(ws *worker.AccountService, ps *photo.Service) {
	session.Default().IncrementPhotoTasksAwaiting()
	photoQueue.Enqueue(&tasks.PhotoFullCommand{
		WorkerService: ws,
		PhotoService:  ps,
	})
}

func enqueueCrawl(as *fb.AccountService, account fb.Account, ws *worker.AccountService, depth int, maxPhotos int, minPhotos int) {
	as.Save(&account)
	enqueueCrawlNoAccount(as, ws, depth, maxPhotos, minPhotos)
}

func enqueueCrawlNoAccount(as *fb.AccountService, ws *worker.AccountService, depth int, maxPhotos int, minPhotos int) {
	session.Default().IncrementAccountTasksAwaiting()
	taskQueue.Enqueue(&RecursCommand{
		WorkerService:  ws,
		AccountService: as,
		Depth:          depth,
		MaxPhotos:      maxPhotos,
		MinPhotos:      minPhotos,
	})
}

var ps *place.Service
var cs *CountryService
var photoService *photo.Service

func stdResolve(s string) *place.Place {
	decoded := s
	for strings.Contains(decoded, "\\") {
		json.Unmarshal([]byte("\""+decoded+"\""), &decoded)
	}
	pl, err := ps.FindByNameOrCreate(decoded, func(p *place.Place) {
		//TODO: "go" this shit for the sake of performance
		go func(p *place.Place) {
			gm := google.MapsHttp{}
			coords, err := gm.FindByName(p.Name)
			if err != nil {
				logging.LogError(err)
			}
			p.Location[0] = coords.X
			p.Location[1] = coords.Y
			p.Country, err = cs.GetCountryNameFromPoint(coords)
			if err != nil {
				logging.LogError(err)
			}
			ps.Save(p)
		}(p)
	})
	if err != nil {
		logging.LogError(err)
	}
	return pl
}

var currentTestID = "100003375601240"

func main() {
	var err error

	dbString := "mongodb://127.0.0.1:27017/parser"
	opts := options.Client()
	connString, err := connstring.Parse(dbString)
	if err != nil || connString.Database == "" {
		panic(err)
	}
	opts.ApplyURI(dbString)
	opts.Auth = nil

	client, err := mongo.Connect(nil, opts)

	if err != nil {
		panic(err)
	}

	db := client.Database(connString.Database)

	ps = place.NewService(db.Collection("Places"))
	cs = &CountryService{db.Collection("Countries")}
	ws := worker.NewAccountService(db.Collection("Workers"))
	//as := fb.NewAccountService(db.Collection("Accounts"))

	photoService = photo.NewService(db.Collection("Photos"))

	worker.ResolvePlace = stdResolve

	taskQueue = queue.NewQueue(1)
	taskQueue.Run()
	photoQueue = queue.NewQueue(1)
	photoQueue.Run()

	err = tasks.PhotoFullCommand{
		PhotoService:photoService,
		WorkerService:ws,
	}.Handle()
	time.Sleep(100 * time.Second)
}
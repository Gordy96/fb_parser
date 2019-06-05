package main

import (
	"encoding/json"
	"errors"
	"flag"
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
	sessionPkg "github.com/gordy96/fb_parser/pkg/session"
	"github.com/gordy96/fb_parser/pkg/tasks"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/x/network/connstring"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type Suspender interface {
	Suspend()
}

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
	session.IncrementAccountsAdded()
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
		session.DecrementAccountTasksAwaiting()
		session.IncrementAccountTasksDone()
	}()

	for photoQueue.Enqueued > 10 {
		sleepMillis(500)
	}

	acc, _ := r.AccountService.FindNextToProcess()
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
	session.IncrementPhotoTasksAwaiting()
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
	session.IncrementAccountTasksAwaiting()
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

func main() {
	var err error

	dbString := flag.String(
		"db",
		"mongodb://127.0.0.1:27017/parser",
		"use a valid mongodb connection uri mongodb://[username:password@]host[:port][/[database]]",
	)
	errLogString := flag.String(
		"errlog",
		fmt.Sprintf("%s_err.log", time.Now().Format("20060102")),
		"filename or 'stderr' keyword (daily rotation files used by default) for error logging",
	)

	logString := flag.String(
		"log",
		fmt.Sprintf("%s.log", time.Now().Format("20060102")),
		"filename or 'stdout' keyword (daily rotation files used by default) for logging",
	)

	//proxiesAddMode := flag.Bool("p", false, "sets working mode to add proxies")
	workersAddMode := flag.Bool("w", false, "sets working mode to add worker accounts")
	crawlMode := flag.Bool("c", false, "sets working mode to fb crawl")

	fileMode := flag.Bool("f", false, "together with -p or -w sets to working with files")

	sessionLog := flag.Bool("l", false, "sets session logging on")

	accountWorkersCount := flag.Int("aw", 1, "number of queue workers for account parsing")
	photoWorkersCount := flag.Int("pw", 1, "number of queue workers for photo downloading")
	crawlDepth := flag.Int("depth", 1, "depth of friends crawl")
	maxPhotos := flag.Int("max_photo", 300, "maximum photos to download per one user")

	minPhotos := flag.Int("min_photo", 30, "minimum photos user must have")

	flag.StringVar(&google.ProxyString, "geo_proxy", "", "proxy for google maps place resolver")

	flag.Parse()

	args := flag.Args()

	opts := options.Client()
	connString, err := connstring.Parse(*dbString)
	if err != nil || connString.Database == "" {
		panic(err)
	}
	opts.ApplyURI(*dbString)
	opts.Auth = nil

	client, err := mongo.Connect(nil, opts)

	if err != nil {
		panic(err)
	}

	db := client.Database(connString.Database)

	if *errLogString != "stderr" {
		f, err := os.OpenFile(fmt.Sprintf("./%s", *errLogString), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0777)
		if err != nil {
			panic(err)
		}
		logging.Errlog = log.New(f, "", 0)
		defer f.Close()
	}

	if *logString != "stdout" {
		f, err := os.OpenFile(fmt.Sprintf("./%s", *logString), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0777)
		if err != nil {
			panic(err)
		}
		logging.Logger = log.New(f, "", 0)
		defer f.Close()
	}

	if *sessionLog {
		go func() {
			for {
				logging.LogAnything(session.String())
				time.Sleep(5 * time.Second)
			}
		}()
	}

	ps = place.NewService(db.Collection("Places"))
	cs = &CountryService{db.Collection("Countries")}
	ws := worker.NewAccountService(db.Collection("Workers"))
	as := fb.NewAccountService(db.Collection("Accounts"))

	photoService = photo.NewService(db.Collection("Photos"))

	worker.ResolvePlace = stdResolve

	if *crawlMode {
		taskQueue = queue.NewQueue(*accountWorkersCount)
		taskQueue.Run()
		photoQueue = queue.NewQueue(*photoWorkersCount)
		photoQueue.Run()
		if !*fileMode {
			if len(args) > 0 {
				for _, arg := range args {
					var acc fb.Account
					if isNumeric(arg) {
						acc = fb.Account{ID: arg}
					} else {
						acc = fb.Account{Nickname: arg}
					}
					enqueueCrawl(as, acc, ws, *crawlDepth, *maxPhotos, *minPhotos)
				}
			} else {
				for i := 0; i < *accountWorkersCount; i++ {
					enqueueCrawlNoAccount(as, ws, *crawlDepth, *maxPhotos, *minPhotos)
				}
				for i := 0; i < *photoWorkersCount; i++ {
					enqueueFullNoPhoto(ws, photoService)
				}
			}
		}
	} else if *workersAddMode {
		if !*fileMode {
			vrx := regexp.MustCompile("([^:]+):([^|]+)(?:\\|((?:(?:(?:https?)|(?:socks(?:4|5))):\\/\\/)?(?:(.+?):(.+?)@)?(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?):[0-9]{2,5}))?")
			for _, arg := range args {
				matches := vrx.FindAllSubmatch([]byte(arg), -1)
				if len(matches) == 1 {
					email := string(matches[0][1])
					password := string(matches[0][2])
					proxy := string(matches[0][3])
					wrk := worker.NewFBAccount(email, password, proxy)
					err = wrk.Login()
					if err != nil {
						logging.LogError(err)
						logging.LogAnything(fmt.Sprintf("error occured on adding %s (see error log)", wrk.Email))
					} else {
						logging.LogAnything(fmt.Sprintf("found and saved %s", wrk.Email))
						ws.Save(wrk)
					}
				}
			}
		}
	}

	if photoQueue != nil && taskQueue != nil {
		go func() {
			if session.PhotoTasksAwaiting == 0 {
				enqueueFullNoPhoto(ws, photoService)
			}
			if session.AccountTasksAwaiting == 0 {
				enqueueCrawlNoAccount(as, ws, *crawlDepth, *minPhotos, *maxPhotos)
			}
			sleepMillis(5 * 1000 * 60)
		}()

		//waits for queues to end work
		for taskQueue.RunningWorkersCount() > 0 || photoQueue.RunningWorkersCount() > 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	logging.LogAnything(session.String())
	logging.LogAnything("program exited")
}

func isNumeric(s string) bool {
	for _, c := range s {
		if unicode.IsLetter(c) {
			return false
		}
	}
	return true
}

var session = sessionPkg.Default()

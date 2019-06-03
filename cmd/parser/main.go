package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/gordy96/fb_parser/pkg/fb"
	"github.com/gordy96/fb_parser/pkg/fb/util"
	"github.com/gordy96/fb_parser/pkg/fb/worker"
	errors2 "github.com/gordy96/fb_parser/pkg/fb/worker/errors"
	"github.com/gordy96/fb_parser/pkg/geo"
	"github.com/gordy96/fb_parser/pkg/geo/google"
	"github.com/gordy96/fb_parser/pkg/queue"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/x/network/connstring"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Suspender interface {
	Suspend()
}

type AccountService struct {
	col *mongo.Collection
	mux sync.Mutex
}

func (a *AccountService) Find(id string) *fb.Account {
	r := a.col.FindOne(nil, bson.M{
		"id": id,
	})
	b := &fb.Account{}

	e := r.Decode(b)

	if e != nil {
		return nil
	}
	return b
}

func (a *AccountService) Save(account *fb.Account) (bool, error) {
	o := &options.UpdateOptions{}
	o.SetUpsert(true)
	a.mux.Lock()
	if account.CreatedAt == 0 {
		account.CreatedAt = time.Now().Unix()
	}
	if account.Status == "" {
		account.Status = fb.Unprocessed
	}
	r, err := a.col.UpdateOne(nil, bson.M{"id": account.ID}, bson.M{"$set": account}, o)
	a.mux.Unlock()
	return (r.ModifiedCount + r.UpsertedCount) >= 1, err
}

func (a *AccountService) Delete(account *fb.Account) (bool, error) {
	r, err := a.col.DeleteOne(nil, bson.M{"id": account.ID})
	return (r.DeletedCount) >= 1, err
}

func (a *AccountService) FindNextToProcess() (*fb.Account, error) {
	r := a.col.FindOneAndUpdate(nil, bson.M{
		"status": fb.Unprocessed,
	}, bson.M{
		"$set": bson.M{
			"status": fb.Processing,
		},
	})
	b := &fb.Account{}

	e := r.Decode(b)

	if e != nil {
		return nil, e
	}
	return b, nil
}

type WorkerAccountService struct {
	col *mongo.Collection
	mux sync.Mutex
}

func (w *WorkerAccountService) find(criteria bson.M) (*worker.FBAccount, error) {
	r := w.col.FindOneAndUpdate(nil, criteria, bson.M{"$set": bson.M{"status": worker.Busy}})
	b := &worker.FBAccount{}

	e := r.Decode(b)
	b.Init()

	if e != nil {
		return nil, e
	}
	return b, nil
}

func (w *WorkerAccountService) FindNextRandom() (*worker.FBAccount, error) {
	a, err := w.find(bson.M{
		"status": worker.Available,
	})
	if err != nil {
		if err == mongo.ErrNoDocuments {
			_, err := w.find(bson.M{
				"$or": bson.A{
					bson.M{"status": worker.Busy},
					bson.M{"status": worker.Available},
				},
			})
			if err != nil && err == mongo.ErrNoDocuments {
				return nil, err
			}
			return nil, nil
		} else {
			return nil, err
		}
	}

	return a, nil
}

func (w *WorkerAccountService) Release(account *worker.FBAccount) (bool, error) {
	account.Status = worker.Available
	return w.Save(account)
}

func (w *WorkerAccountService) Disable(account *worker.FBAccount) (bool, error) {
	account.Status = worker.Error
	return w.Save(account)
}

func (w *WorkerAccountService) FindByEmail(email string) (*worker.FBAccount, error) {
	return w.find(bson.M{
		"email": email,
	})
}

func (w *WorkerAccountService) FindByID(id primitive.ObjectID) (*worker.FBAccount, error) {
	return w.find(bson.M{
		"_id": id,
	})
}

func (w *WorkerAccountService) Save(account *worker.FBAccount) (bool, error) {
	o := options.Update()
	o.SetUpsert(true)
	w.mux.Lock()
	if account.CreatedAt == 0 {
		account.CreatedAt = time.Now().Unix()
	}
	r, err := w.col.UpdateOne(nil, bson.M{"email": account.Email}, bson.M{"$set": account}, o)
	w.mux.Unlock()
	return (r.ModifiedCount + r.UpsertedCount) >= 1, err
}

type PlaceService struct {
	col  *mongo.Collection
	smux sync.Mutex
	fmux sync.Mutex
}

func (p *PlaceService) FindByName(name string) (*fb.Place, error) {
	r := p.col.FindOne(nil, bson.M{"name": name})

	if r.Err() != nil {
		return nil, r.Err()
	}

	place := &fb.Place{}

	err := r.Decode(place)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return place, nil
}

func (p *PlaceService) FindByNameOrCreate(name string, cbl ...func(*fb.Place)) (*fb.Place, error) {
	p.fmux.Lock()
	defer p.fmux.Unlock()
	pl, err := p.FindByName(name)
	if err != nil {
		return nil, err
	}
	if pl != nil {
		return pl, nil
	}
	pl = &fb.Place{Name: name}
	ok, err := p.Save(pl)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("could not save place")
	}
	if len(cbl) > 0 {
		for _, cb := range cbl {
			cb(pl)
		}
	}
	return pl, nil
}

func (p *PlaceService) FindByID(id primitive.ObjectID) (*fb.Place, error) {
	r := p.col.FindOne(nil, bson.M{"_id": id})

	if r.Err() != nil {
		return nil, r.Err()
	}

	place := &fb.Place{}

	err := r.Decode(place)

	if err != nil {
		return nil, err
	}

	return place, nil
}

func (p *PlaceService) Save(place *fb.Place) (bool, error) {
	o := options.Update()
	o.SetUpsert(true)

	var err error
	var insCount int
	p.smux.Lock()
	if place.ID.IsZero() {
		place.ID = primitive.NewObjectID()
	}
	var r *mongo.UpdateResult
	r, err = p.col.UpdateOne(nil, bson.M{"_id": place.ID}, bson.M{"$set": place}, o)
	p.smux.Unlock()
	insCount = int(r.ModifiedCount + r.UpsertedCount)

	if err != nil {
		return false, err
	}

	return insCount >= 1, nil
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

type PhotoStatus string

const (
	Unprocessed PhotoStatus = "unprocessed"
	Processing  PhotoStatus = "processing"
	Processed   PhotoStatus = "processed"
	Error		PhotoStatus	= "error"
)

type Photo struct {
	ID        string      `json:"id" bson:"id"`
	UserID    string      `json:"user_id" bson:"user_id"`
	AlbumID   string      `json:"album_id" bson:"album_id"`
	FullLink  string      `json:"full_link,omitempty" bson:"full_link,omitempty"`
	CreatedAt int64       `json:"created_at" bson:"created_at"`
	Status    PhotoStatus `json:"status" bson:"status"`
}

type PhotoService struct {
	col *mongo.Collection
	mux sync.Mutex
}

func (p *PhotoService) find(criteria interface{}) (*Photo, error) {
	r := p.col.FindOne(nil, criteria)
	photo := &Photo{}
	err := r.Decode(photo)
	if err != nil {
		return nil, err
	}
	return photo, nil
}

func (p *PhotoService) FindByID(id string) (*Photo, error) {
	p.mux.Lock()
	defer p.mux.Unlock()
	return p.find(bson.M{"id": id})
}

func (p *PhotoService) FindNextToDownload() (*Photo, error) {
	p.mux.Lock()
	defer p.mux.Unlock()
	o := options.FindOneAndUpdate()
	o.SetSort(bson.M{
		"_id": -1,
	})
	r := p.col.FindOneAndUpdate(nil, bson.M{
		"full_link": bson.M{
			"$exists": false,
		},
		"status": Unprocessed,
	}, bson.M{
		"$set": bson.M{
			"status": Processing,
		},
	}, o)
	photo := &Photo{}
	err := r.Decode(photo)
	if err != nil {
		return nil, err
	}
	return photo, nil
}

func (p *PhotoService) Save(photo *Photo) (bool, error) {
	p.mux.Lock()
	defer p.mux.Unlock()
	o := options.Update()
	o.SetUpsert(true)
	if photo.CreatedAt == 0 {
		photo.CreatedAt = time.Now().Unix()
	}
	if photo.Status == "" {
		photo.Status = Unprocessed
	}
	re, err := p.col.UpdateOne(nil, bson.M{"id": photo.ID}, bson.M{"$set": photo}, o)
	if err != nil {
		return false, err
	}
	return (re.ModifiedCount + re.UpsertedCount) > 0, nil
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
			f.Write(content)
			f.Close()
		}
	}
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

func recursiveSearch(as *AccountService, account *fb.Account, ws *WorkerAccountService, worker *worker.FBAccount, depth int, maxPhotos int, minPhotos int) error {
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
			logError(e)
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
				logError(e)
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
							enqueuePhotoFull(ws, photoService, Photo{ID: id, AlbumID: album, UserID: account.ID})
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
					logError(e)
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
	logAnything(fmt.Sprintf("%s[%s]: %d photos, %d freinds", account.Nickname, account.ID, foundPhotos, foundFriends))
	return nil
}

var logMux = sync.Mutex{}
var errlog *log.Logger = log.New(os.Stderr, "", 0)
var logger *log.Logger = log.New(os.Stdout, "", 0)

func logError(e error) {
	logMux.Lock()
	defer logMux.Unlock()
	l := func(err error, req []byte, resp []byte) {
		errlog.Printf(
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
	case errors2.ParsingError:
		l(err, err.Request, err.Response)
	case errors2.NoFriendsError:
		l(err, err.Request, err.Response)
	case errors2.NoAlbumsError:
		l(err, err.Request, err.Response)
	case errors2.GenderUndefinedError:
		l(err, err.Request, err.Response)
	case errors2.WorkerCheckpointError:
		l(err, err.Request, err.Response)
	case errors2.BrokenLinkCheckpoint:
		l(err, err.Request, err.Response)
	case errors2.AuthenticationFailedError:
		l(err, err.Request, err.Response)
	default:
		errlog.Printf("[%s] ERROR: %v\n", time.Now().Format(time.RFC3339), err)
	}
}

func logAnything(v interface{}) {
	logMux.Lock()
	defer logMux.Unlock()
	logger.Printf("[%s] INFO: %v\n", time.Now().Format(time.RFC3339), v)
}

type RecursCommand struct {
	WorkerService  *WorkerAccountService
	AccountService *AccountService
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
		logAnything(fmt.Sprintf("no account to process"))
		return nil
	}

	for time.Now().Sub(start) < 15*time.Minute {
		w, err := r.WorkerService.FindNextRandom()
		if err != nil {
			logError(err)
			logAnything("worker stops see error log")
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
				logError(err)
				logAnything(fmt.Sprintf("worker %s got critical exception. See logs", w.Email))
				switch err.(type) {
				case errors2.WorkerCheckpointError:
					logAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
					r.WorkerService.Disable(w)
					continue
				case errors2.BrokenLinkCheckpoint:
					logAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
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

type PhotoFullCommand struct {
	WorkerService *WorkerAccountService
	PhotoService  *PhotoService
}

func (p PhotoFullCommand) Handle() error {
	defer func() {
		session.DecrementPhotoTasksAwaiting()
		session.IncrementPhotoTasksDone()
	}()

	photo, _ := p.PhotoService.FindNextToDownload()

	if photo == nil {
		logAnything("no photos to download")
		return nil
	}

	start := time.Now()
	for time.Now().Sub(start) < 15*time.Minute {
		w, err := p.WorkerService.FindNextRandom()
		if err != nil {
			logError(err)
			logAnything("worker stops see error log")
			photo.Status = Unprocessed
			p.PhotoService.Save(photo)
			return nil
		}

		if w != nil {
			//logAnything(fmt.Sprintf("got worker %s", w.Email))
			w.Init()
			fullLink, err := w.GetPhotoFull(photo.ID)
			if err != nil {
				logError(err)
				logAnything(fmt.Sprintf("worker %s got critical exception. See logs", w.Email))

				switch err.(type) {
				case errors2.WorkerCheckpointError:
					logAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
					p.WorkerService.Disable(w)
					continue
				case errors2.BrokenLinkCheckpoint:
					logAnything(fmt.Sprintf("worker %s got checkpoint", w.Email))
					p.WorkerService.Disable(w)
					continue
				}
				//logAnything(fmt.Sprintf("releasing worker %s", w.Email))
				p.WorkerService.Release(w)
				photo.Status = Unprocessed
				p.PhotoService.Save(photo)
				return nil
			}
			//logAnything(fmt.Sprintf("releasing worker %s", w.Email))
			p.WorkerService.Release(w)
			if fullLink != "" {
				photo.FullLink = fullLink
				photo.Status = Processed
				p.PhotoService.Save(photo)
				session.IncrementPhotosDownloaded()
				//Removed parallelism so no "too many files" exception raised
				go SaveFullPhoto(photo.UserID, photo.AlbumID, photo.ID, fullLink)
			} else {
				photo.Status = Unprocessed
				p.PhotoService.Save(photo)
			}
			return nil
		}
		sleepMillis(20)
	}
	photo.Status = Unprocessed
	p.PhotoService.Save(photo)
	logError(errors.New(fmt.Sprintf("couldn't acquire worker account saving %s/%s/%s", photo.UserID, photo.AlbumID, photo.ID)))
	return nil
}

var taskQueue *queue.Queue
var photoQueue *queue.Queue

func enqueuePhotoFull(ws *WorkerAccountService, ps *PhotoService, p Photo) {
	ps.Save(&p)
	enqueueFullNoPhoto(ws, ps)
}

func enqueueFullNoPhoto(ws *WorkerAccountService, ps *PhotoService) {
	session.IncrementPhotoTasksAwaiting()
	photoQueue.Enqueue(&PhotoFullCommand{
		WorkerService: ws,
		PhotoService:  ps,
	})
}

func enqueueCrawl(as *AccountService, account fb.Account, ws *WorkerAccountService, depth int, maxPhotos int, minPhotos int) {
	as.Save(&account)
	enqueueCrawlNoAccount(as, ws, depth, maxPhotos, minPhotos)
}

func enqueueCrawlNoAccount(as *AccountService, ws *WorkerAccountService, depth int, maxPhotos int, minPhotos int) {
	session.IncrementAccountTasksAwaiting()
	taskQueue.Enqueue(&RecursCommand{
		WorkerService:  ws,
		AccountService: as,
		Depth:          depth,
		MaxPhotos:      maxPhotos,
		MinPhotos:      minPhotos,
	})
}

var ps *PlaceService
var cs *CountryService
var photoService *PhotoService

func stdResolve(s string) *fb.Place {
	decoded := s
	for strings.Contains(decoded, "\\") {
		json.Unmarshal([]byte("\""+decoded+"\""), &decoded)
	}
	place, err := ps.FindByNameOrCreate(decoded, func(place *fb.Place) {
		//TODO: "go" this shit for the sake of performance
		go func(place *fb.Place) {
			gm := google.MapsHttp{}
			coords, err := gm.FindByName(place.Name)
			if err != nil {
				logError(err)
			}
			place.Location[0] = coords.X
			place.Location[1] = coords.Y
			place.Country, err = cs.GetCountryNameFromPoint(coords)
			if err != nil {
				logError(err)
			}
			ps.Save(place)
		}(place)
	})
	if err != nil {
		logError(err)
	}
	return place
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
		errlog = log.New(f, "", 0)
		defer f.Close()
	}

	if *logString != "stdout" {
		f, err := os.OpenFile(fmt.Sprintf("./%s", *logString), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0777)
		if err != nil {
			panic(err)
		}
		logger = log.New(f, "", 0)
		defer f.Close()
	}

	if *sessionLog {
		go func() {
			for {
				logAnything(session.String())
				time.Sleep(5 * time.Second)
			}
		}()
	}

	ps = &PlaceService{db.Collection("Places"), sync.Mutex{}, sync.Mutex{}}
	cs = &CountryService{db.Collection("Countries")}
	ws := WorkerAccountService{db.Collection("Workers"), sync.Mutex{}}
	as := AccountService{db.Collection("Accounts"), sync.Mutex{}}

	photoService = &PhotoService{db.Collection("Photos"), sync.Mutex{}}

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
					enqueueCrawl(&as, acc, &ws, *crawlDepth, *maxPhotos, *minPhotos)
				}
			} else {
				for i := 0; i < *accountWorkersCount; i++ {
					enqueueCrawlNoAccount(&as, &ws, *crawlDepth, *maxPhotos, *minPhotos)
				}
				for i := 0; i < *photoWorkersCount; i++ {
					enqueueFullNoPhoto(&ws, photoService)
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
						logError(err)
						logAnything(fmt.Sprintf("error occured on adding %s (see error log)", wrk.Email))
					} else {
						logAnything(fmt.Sprintf("found and saved %s", wrk.Email))
						ws.Save(wrk)
					}
				}
			}
		}
	}

	if photoQueue != nil && taskQueue != nil {
		go func() {
			if session.PhotoTasksAwaiting == 0 {
				enqueueFullNoPhoto(&ws, photoService)
			}
			if session.AccountTasksAwaiting == 0 {
				enqueueCrawlNoAccount(&as, &ws, *crawlDepth, *minPhotos, *maxPhotos)
			}
			sleepMillis(5 * 1000 * 60)
		}()

		//waits for queues to end work
		for taskQueue.RunningWorkersCount() > 0 || photoQueue.RunningWorkersCount() > 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	logAnything(session.String())
	logAnything("program exited")
}

func isNumeric(s string) bool {
	for _, c := range s {
		if unicode.IsLetter(c) {
			return false
		}
	}
	return true
}

type SessionStatistics struct {
	AccountsAdded        int `json:"accounts_added"`
	AccountTasksDone     int `json:"account_tasks_done"`
	AccountTasksAwaiting int `json:"account_tasks_awaiting"`

	PhotosDownloaded int `json:"photo_downloaded"`

	PhotoTasksDone     int `json:"photo_tasks_done"`
	PhotoTasksAwaiting int `json:"photo_tasks_awaiting"`

	aamux  sync.Mutex `json:"-"`
	tdmux  sync.Mutex `json:"-"`
	tamux  sync.Mutex `json:"-"`
	pdmux  sync.Mutex `json:"-"`
	ptamux sync.Mutex `json:"-"`
	ptdmux sync.Mutex `json:"-"`
}

func (s *SessionStatistics) IncrementAccountsAdded() {
	s.aamux.Lock()
	s.AccountsAdded++
	s.aamux.Unlock()
}

func (s *SessionStatistics) IncrementAccountTasksDone() {
	s.tdmux.Lock()
	s.AccountTasksDone++
	s.tdmux.Unlock()
}

func (s *SessionStatistics) IncrementPhotosDownloaded() {
	s.pdmux.Lock()
	s.PhotosDownloaded++
	s.pdmux.Unlock()
}

func (s *SessionStatistics) IncrementAccountTasksAwaiting() {
	s.tamux.Lock()
	s.AccountTasksAwaiting++
	s.tamux.Unlock()
}

func (s *SessionStatistics) DecrementAccountTasksAwaiting() {
	s.tamux.Lock()
	s.AccountTasksAwaiting--
	s.tamux.Unlock()
}

func (s *SessionStatistics) IncrementPhotoTasksAwaiting() {
	s.ptamux.Lock()
	s.PhotoTasksAwaiting++
	s.ptamux.Unlock()
}

func (s *SessionStatistics) DecrementPhotoTasksAwaiting() {
	s.ptamux.Lock()
	s.PhotoTasksAwaiting--
	s.ptamux.Unlock()
}

func (s *SessionStatistics) IncrementPhotoTasksDone() {
	s.ptdmux.Lock()
	s.PhotoTasksDone++
	s.ptdmux.Unlock()
}

func (s *SessionStatistics) String() string {
	b, _ := json.Marshal(s)
	return string(b)
}

var session = SessionStatistics{
	0,
	0,
	0,
	0,
	0,
	0,
	sync.Mutex{},
	sync.Mutex{},
	sync.Mutex{},
	sync.Mutex{},
	sync.Mutex{},
	sync.Mutex{},
}

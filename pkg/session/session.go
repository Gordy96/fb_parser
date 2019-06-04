package session

import (
	"encoding/json"
	"sync"
)

type Statistics struct {
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

func (s *Statistics) IncrementAccountsAdded() {
	s.aamux.Lock()
	s.AccountsAdded++
	s.aamux.Unlock()
}

func (s *Statistics) IncrementAccountTasksDone() {
	s.tdmux.Lock()
	s.AccountTasksDone++
	s.tdmux.Unlock()
}

func (s *Statistics) IncrementPhotosDownloaded() {
	s.pdmux.Lock()
	s.PhotosDownloaded++
	s.pdmux.Unlock()
}

func (s *Statistics) IncrementAccountTasksAwaiting() {
	s.tamux.Lock()
	s.AccountTasksAwaiting++
	s.tamux.Unlock()
}

func (s *Statistics) DecrementAccountTasksAwaiting() {
	s.tamux.Lock()
	s.AccountTasksAwaiting--
	s.tamux.Unlock()
}

func (s *Statistics) IncrementPhotoTasksAwaiting() {
	s.ptamux.Lock()
	s.PhotoTasksAwaiting++
	s.ptamux.Unlock()
}

func (s *Statistics) DecrementPhotoTasksAwaiting() {
	s.ptamux.Lock()
	s.PhotoTasksAwaiting--
	s.ptamux.Unlock()
}

func (s *Statistics) IncrementPhotoTasksDone() {
	s.ptdmux.Lock()
	s.PhotoTasksDone++
	s.ptdmux.Unlock()
}

func (s *Statistics) String() string {
	b, _ := json.Marshal(s)
	return string(b)
}

func NewSession() *Statistics {
	return &Statistics{
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
}

var defaultSession *Statistics = nil
var mux = sync.Mutex{}
func Default() *Statistics {
	mux.Lock()
	if defaultSession == nil {
		defaultSession = NewSession()
	}
	mux.Unlock()
	return defaultSession
}
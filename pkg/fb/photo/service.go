package photo

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"sync"
	"time"
)

func NewService(col* mongo.Collection) *Service {
	return &Service{
		col,
		sync.Mutex{},
	}
}

type Service struct {
	col *mongo.Collection
	mux sync.Mutex
}

func (p *Service) find(criteria interface{}) (*Photo, error) {
	r := p.col.FindOne(nil, criteria)
	photo := &Photo{}
	err := r.Decode(photo)
	if err != nil {
		return nil, err
	}
	return photo, nil
}

func (p *Service) FindByID(id string) (*Photo, error) {
	p.mux.Lock()
	defer p.mux.Unlock()
	return p.find(bson.M{"id": id})
}

func (p *Service) FindNextToDownload() (*Photo, error) {
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

func (p *Service) Save(photo *Photo) (bool, error) {
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
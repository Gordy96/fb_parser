package place

import (
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"sync"
)


func NewService(col *mongo.Collection) *Service {
	return &Service {
		col,
		sync.Mutex{},
		sync.Mutex{},
	}
}

type Service struct {
	col  *mongo.Collection
	smux sync.Mutex
	fmux sync.Mutex
}

func (p *Service) FindByName(name string) (*Place, error) {
	r := p.col.FindOne(nil, bson.M{"name": name})

	if r.Err() != nil {
		return nil, r.Err()
	}

	place := &Place{}

	err := r.Decode(place)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return place, nil
}

func (p *Service) FindByNameOrCreate(name string, cbl ...func(*Place)) (*Place, error) {
	p.fmux.Lock()
	defer p.fmux.Unlock()
	pl, err := p.FindByName(name)
	if err != nil {
		return nil, err
	}
	if pl != nil {
		return pl, nil
	}
	pl = &Place{Name: name}
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

func (p *Service) FindByID(id primitive.ObjectID) (*Place, error) {
	r := p.col.FindOne(nil, bson.M{"_id": id})

	if r.Err() != nil {
		return nil, r.Err()
	}

	place := &Place{}

	err := r.Decode(place)

	if err != nil {
		return nil, err
	}

	return place, nil
}

func (p *Service) Save(place *Place) (bool, error) {
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

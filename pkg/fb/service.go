package fb

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"sync"
	"time"
)

func NewAccountService(col *mongo.Collection) *AccountService {
	return &AccountService {
		col,
		sync.Mutex{},
	}
}

type AccountService struct {
	col *mongo.Collection
	mux sync.Mutex
}

func (a *AccountService) Find(id string) (*Account, error) {
	r := a.col.FindOne(nil, bson.M{
		"id": id,
	})
	b := &Account{}

	e := r.Decode(b)

	if e != nil {
		return nil, e
	}
	return b, nil
}

func (a *AccountService) Save(account *Account) (bool, error) {
	o := &options.UpdateOptions{}
	o.SetUpsert(true)
	a.mux.Lock()
	if account.CreatedAt == 0 {
		account.CreatedAt = time.Now().Unix()
	}
	if account.Status == "" {
		account.Status = Unprocessed
	}
	r, err := a.col.UpdateOne(nil, bson.M{"id": account.ID}, bson.M{"$set": account}, o)
	a.mux.Unlock()
	return (r.ModifiedCount + r.UpsertedCount) >= 1, err
}

func (a *AccountService) Delete(account *Account) (bool, error) {
	r, err := a.col.DeleteOne(nil, bson.M{"id": account.ID})
	return (r.DeletedCount) >= 1, err
}

func (a *AccountService) FindNextToProcess() (*Account, error) {
	r := a.col.FindOneAndUpdate(nil, bson.M{
		"status": Unprocessed,
	}, bson.M{
		"$set": bson.M{
			"status": Processing,
		},
	})
	b := &Account{}

	e := r.Decode(b)

	if e != nil {
		return nil, e
	}
	return b, nil
}

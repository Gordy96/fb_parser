package worker

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
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

func (w *AccountService) find(criteria bson.M) (*FBAccount, error) {
	r := w.col.FindOneAndUpdate(nil, criteria, bson.M{"$set": bson.M{"status": Busy}})
	b := &FBAccount{}

	e := r.Decode(b)
	b.Init()

	if e != nil {
		return nil, e
	}
	return b, nil
}

func (w *AccountService) FindNextRandom() (*FBAccount, error) {
	a, err := w.find(bson.M{
		"status": Available,
	})
	if err != nil {
		if err == mongo.ErrNoDocuments {
			_, err := w.find(bson.M{
				"$or": bson.A{
					bson.M{"status": Busy},
					bson.M{"status": Available},
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

func (w *AccountService) Release(account *FBAccount) (bool, error) {
	account.Status = Available
	return w.Save(account)
}

func (w *AccountService) Disable(account *FBAccount) (bool, error) {
	account.Status = Error
	return w.Save(account)
}

func (w *AccountService) FindByEmail(email string) (*FBAccount, error) {
	return w.find(bson.M{
		"email": email,
	})
}

func (w *AccountService) FindByID(id primitive.ObjectID) (*FBAccount, error) {
	return w.find(bson.M{
		"_id": id,
	})
}

func (w *AccountService) Save(account *FBAccount) (bool, error) {
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

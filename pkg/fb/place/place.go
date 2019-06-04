package place

import "go.mongodb.org/mongo-driver/bson/primitive"

type Place struct {
	ID        primitive.ObjectID `json:"id" bson:"_id"`
	Name      string             `json:"name" bson:"name"`
	Location  [2]float64         `json:"location" bson:"location"`
	Country   string             `json:"country" bson:"country"`
	CreatedAt int64              `json:"created_at" bson:"created_at"`
}

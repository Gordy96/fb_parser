package fb

import "go.mongodb.org/mongo-driver/bson/primitive"

type Gender int

const (
	Unknown Gender = iota
	Male
	Female
)

type Status string

const (
	Unprocessed Status = "unprocessed"
	Processing  Status = "processing"
	Processed   Status = "processed"
)

type Account struct {
	ID            string               `json:"id" bson:"id"`
	Nickname      string               `json:"nickname" bson:"nickname"`
	FirstName     string               `json:"first_name" bson:"first_name"`
	LastName      string               `json:"last_name" bson:"last_name"`
	Places        []primitive.ObjectID `json:"places" bson:"places"`
	Friends       []string             `json:"friends" bson:"friends"`
	Hometown      primitive.ObjectID   `json:"hometown,omitempty" bson:"hometown,omitempty"`
	CurrentCity   primitive.ObjectID   `json:"current_city,omitempty" bson:"current_city,omitempty"`
	Gender        Gender               `json:"gender" bson:"gender"`
	PhotosParsed  bool                 `json:"photos_parsed" bson:"photos_parsed"`
	FriendsParsed bool                 `json:"friends_parsed" bson:"friends_parsed"`
	Status        Status               `json:"status" bson:"status"`
	CreatedAt     int64                `json:"created_at" bson:"created_at"`
}

type MalformedAccountError struct {
	Account *Account
}

func (u MalformedAccountError) Error() string {
	return "malformed account"
}

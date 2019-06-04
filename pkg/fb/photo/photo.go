package photo

type Status string

const (
	Unprocessed Status = "unprocessed"
	Processing  Status = "processing"
	Processed   Status = "processed"
	Error		Status	= "error"
)

type Photo struct {
	ID        string      `json:"id" bson:"id"`
	UserID    string      `json:"user_id" bson:"user_id"`
	AlbumID   string      `json:"album_id" bson:"album_id"`
	FullLink  string      `json:"full_link,omitempty" bson:"full_link,omitempty"`
	CreatedAt int64       `json:"created_at" bson:"created_at"`
	Status    Status		`json:"status" bson:"status"`
}
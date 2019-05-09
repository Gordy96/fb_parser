package geo

import "fmt"

type InTheMiddleOfNoWhereError struct {
	Point Point
}

func (i InTheMiddleOfNoWhereError) Error() string {
	return fmt.Sprintf("no country intersects this coordinates [%f, %f]", i.Point.X, i.Point.Y)
}

type Point struct {
	X float64		`json:"x" bson:"x"`
	Y float64		`json:"y" bson:"y"`
}

type LocationProvider interface {
	FindByName(string) (Point, error)
}

type CountryChecker interface {
	GetCountryNameFromPoint(Point) (string, error)
}
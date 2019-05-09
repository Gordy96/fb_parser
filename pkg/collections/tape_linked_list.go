package collections

import (
	"errors"
	"sync"
)

type TapeEntry struct {
	Value 		interface{}
	Previous	*TapeEntry
	Next		*TapeEntry
}

type TapeConveyor struct {
	First		*TapeEntry
	Last		*TapeEntry
	Current		*TapeEntry
	mux			sync.Mutex
}

func (w *TapeConveyor) Add(a interface{}) {
	w.mux.Lock()
	defer w.mux.Unlock()
	if w.Last == nil {
		if w.First == nil {
			w.First = &TapeEntry{Value: a, Previous: nil, Next: nil}
			w.Last = w.First
			w.Current = w.First
			return
		}
		panic(errors.New("code should not run here"))
	}
	e := &TapeEntry{Value: a, Previous: w.Last, Next: nil}
	w.Last.Next = e
	w.Last = e
}

func (w *TapeConveyor) Next() interface{} {
	w.mux.Lock()
	defer w.mux.Unlock()
	e := w.Current.Value
	if w.Current.Next != nil {
		w.Current = w.Current.Next
	} else {
		w.Current = w.First
	}
	return e
}

func NewTapeConveyor() *TapeConveyor {
	return &TapeConveyor{
		mux: sync.Mutex{},
	}
}

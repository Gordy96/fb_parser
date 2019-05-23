package main

import (
	"fmt"
	"github.com/gordy96/fb_parser/pkg/queue"
	"os"
	"strconv"
	"sync"
	"time"
)

var q1 *queue.Queue = nil
var q2 *queue.Queue = nil

type C1 struct{}

func (c *C1) Handle() error {
	time.Sleep(1000 * time.Millisecond)
	fmt.Printf("[%s] C1 task done on q1\n", time.Now().Format(time.RFC3339))
	return nil
}

type C2 struct{}

func (c *C2) Handle() error {
	for q1.Enqueued > 10 {
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(1000 * time.Millisecond)
	fmt.Printf("[%s] C2 task done on q2\n", time.Now().Format(time.RFC3339))
	return nil
}

func main() {
	wg := sync.WaitGroup{}
	wg.Add(10000)
	for i := 0; i < 10000; i++ {
		go func(w *sync.WaitGroup) {
			now := time.Now().UnixNano()
			f, err := os.OpenFile(fmt.Sprintf("./storage/%d.txt", now), os.O_CREATE|os.O_WRONLY, 0777)
			if err != nil {
				panic(err)
			}

			f.WriteString(strconv.FormatInt(now, 10))
			fmt.Printf("%d\n", now)
			f.Close()
			w.Done()
		}(&wg)
	}
	wg.Wait()
	return
	q1 = queue.NewQueue(10)
	q2 = queue.NewQueue(10)

	q1.Run()
	q2.Run()

	go func() {
		for {
			for i := 0; i < 100; i++ {
				q1.Enqueue(&C1{})
			}
			time.Sleep(30000 * time.Millisecond)
		}
	}()

	go func() {
		for {
			q2.Enqueue(&C2{})
			time.Sleep(5000 * time.Millisecond)
		}
	}()

	for {
		time.Sleep(1000 * time.Millisecond)
	}
}

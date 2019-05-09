package queue

type Command interface {
	Handle() error
}

func NewWorker(pool chan chan Command) *Worker {
	return &Worker{
		pool,
		make(chan Command),
		make(chan bool),
	}
}

type Worker struct {
	pool chan chan Command
	input chan Command
	quit chan bool
}

func (w *Worker) Start() {
	go func() {
		for {
			w.pool <- w.input
			select {
			case job := <- w.input:
				err := job.Handle()
				if err != nil {
					w.Stop()
				}
			case <- w.quit:
				return
			}
		}
	}()
}

func (w *Worker) Stop() {
	go func() {
		w.quit <- true
	}()
}

type Queue struct {
	WorkerCount int
	pool chan chan Command
	jobQueue chan Command
}

func (q *Queue) Run() {
	for i := 0; i < q.WorkerCount; i++ {
		worker := NewWorker(q.pool)
		worker.Start()
	}
	go func() {
		for {
			select {
			case job := <- q.jobQueue:
				go func(c Command) {
					workerChan := <- q.pool
					workerChan <- c
				}(job)
			}
		}
	}()
}

func (q *Queue) Enqueue(c Command) {
	q.jobQueue <- c
}

func NewQueue(numWorkers int) *Queue {
	q := &Queue{
		pool: make(chan chan Command),
		WorkerCount: numWorkers,
		jobQueue: make(chan Command),
	}
	return q
}

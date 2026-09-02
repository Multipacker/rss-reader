package jobs

import (
	"context"
	"log"
	"time"
)

type Job struct {
	Name    string
	Context context.Context
	Logger  *log.Logger
	cancel  context.CancelFunc
	done    chan struct{}
}

func New(name string) *Job {
	logger := log.New(log.Writer(), name + " ", log.Flags())
	context, cancel := context.WithCancel(context.Background())

	return &Job {
		Name:    name,
		Context: context,
		Logger:  logger,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

func (job *Job) Cancel() {
	job.cancel()
}

func (job *Job) Canceled() <-chan struct{} {
	return job.Context.Done()
}

func (job *Job) Finish() {
	close(job.done)
}

func (job *Job) Finished() <-chan struct{} {
	return job.done
}



type Jobs []*Job

func (jobs Jobs) CancelAndWait(timeout time.Duration) []string {
	for _, job := range jobs {
		job.Cancel()
	}

	allDone := make(chan struct{})
	timer := time.NewTimer(timeout)

	go func() {
		for _, job := range jobs {
			<-job.Finished()
		}
		close(allDone)
	}()

	select {
	case <-timer.C:
		return jobs.ListUnfinished()
	case <-allDone:
		return nil
	}
}

func (jobs Jobs) ListUnfinished() []string {
	unfinished := []string{}
	for _, job := range jobs {
		select {
		case <-job.Finished():
			continue
		default:
			unfinished = append(unfinished, job.Name)
		}
	}
	return unfinished
}

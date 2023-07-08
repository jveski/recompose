package concurrency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRunLoop(t *testing.T) {
	t.Run("blocks and cools down when handling signals", func(t *testing.T) {
		signal := make(chan struct{}, 2)
		defer close(signal)

		signal <- struct{}{}
		signal <- struct{}{}

		output := make(chan struct{})
		go RunLoop(signal, time.Hour, time.Second, func() bool {
			output <- struct{}{}
			return true
		})

		start := time.Now()
		<-output
		<-output
		assert.GreaterOrEqual(t, time.Since(start), time.Millisecond*90)
	})

	t.Run("resync", func(t *testing.T) {
		output := make(chan struct{})
		go RunLoop(make(<-chan struct{}), time.Millisecond, time.Second, func() bool {
			output <- struct{}{}
			return true
		})

		<-output
		<-output
	})

	t.Run("retries", func(t *testing.T) {
		output := make(chan struct{})
		go RunLoop(make(<-chan struct{}), time.Millisecond, time.Millisecond*25, func() bool {
			output <- struct{}{}
			return false
		})

		<-output

		start := time.Now()
		<-output
		latencyA := time.Since(start)

		start = time.Now()
		<-output

		<-output
		latencyB := time.Since(start)

		assert.Greater(t, latencyB, latencyA)
	})
}

func TestStateContainer(t *testing.T) {
	s := &StateContainer[int]{}

	ch := make(chan int)
	go func() {
		for range s.Watch(context.Background()) {
			ch <- s.Get()
		}
	}()

	assert.Equal(t, 0, s.Get())
	s.Swap(123)
	assert.Equal(t, 123, s.Get())
}

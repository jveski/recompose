package concurrency

import (
	"context"
	mathrand "math/rand"
	"sync"
	"time"
)

func RunLoop(signal <-chan struct{}, resync, maxRetry time.Duration, fn func() bool) {
	ch := make(chan struct{}, 1)
	ch <- struct{}{} // initial sync

	go func() {
		for range signal {
			ch <- struct{}{}
		}
		close(ch)
	}()

	if resync > 0 {
		go func() {
			timer := time.NewTicker(Jitter(resync))
			for range timer.C {
				select {
				case ch <- struct{}{}:
				default:
				}
				timer.Reset(Jitter(resync))
			}
		}()
	}

	attempt := func() {
		var lastRetry time.Duration
		for {
			if fn() {
				break
			}

			if lastRetry == 0 {
				lastRetry = time.Millisecond * 50
			}
			lastRetry += lastRetry / 8
			if lastRetry > maxRetry {
				lastRetry = maxRetry
			}

			time.Sleep(Jitter(lastRetry))
		}
	}

	for range ch {
		attempt()
		time.Sleep(Jitter(time.Millisecond * 100)) // cooldown
	}
}

func Jitter(duration time.Duration) time.Duration {
	maxJitter := int64(duration) * int64(5) / 100 // 5% jitter
	return duration + time.Duration(mathrand.Int63n(maxJitter*2)-maxJitter)
}

type StateContainer[T any] struct {
	lock     sync.Mutex
	current  T
	watchers map[any]chan struct{}
}

func (s *StateContainer[T]) Get() T {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.current
}

func (s *StateContainer[T]) Swap(val T) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.current = val
	s.bumpUnlocked()
}

func (s *StateContainer[T]) ReEnter() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.bumpUnlocked()
}

func (s *StateContainer[T]) bumpUnlocked() {
	for _, ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *StateContainer[T]) Watch(ctx context.Context) <-chan struct{} {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.watchers == nil {
		s.watchers = map[any]chan struct{}{}
	}

	ch := make(chan struct{}, 1)
	go func() {
		<-ctx.Done()

		s.lock.Lock()
		defer s.lock.Unlock()

		delete(s.watchers, ctx)
		close(ch)
	}()

	s.watchers[ctx] = ch
	return ch
}

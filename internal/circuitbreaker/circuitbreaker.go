// Package circuitbreaker provides a simple circuit breaker implementation
// for outbound HTTP calls to prevent cascade failures.
package circuitbreaker

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State represents the circuit breaker state
type State int

const (
	StateClosed State = iota // Normal operation, requests pass through
	StateOpen                // Failing, requests blocked
	StateHalfOpen            // Testing if service recovered
)

var (
	ErrCircuitOpen   = errors.New("circuit breaker is open")
	ErrTooManyCalls  = errors.New("too many calls in half-open state")
)

// Config holds circuit breaker configuration
type Config struct {
	Name              string
	MaxRequests       uint32        // Max requests in half-open state
	Interval          time.Duration // Time in closed state before reset counts
	Timeout           time.Duration // Time in open state before half-open
	ReadyToTrip       func(counts Counts) bool
	OnStateChange     func(name string, from State, to State)
	IsSuccessful      func(err error) bool
}

// Counts holds the statistics for the circuit breaker
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	name          string
	maxRequests   uint32
	interval      time.Duration
	timeout       time.Duration
	readyToTrip   func(Counts) bool
	onStateChange func(name string, from State, to State)
	isSuccessful  func(err error) bool

	mu       sync.Mutex
	state    State
	counts   Counts
	expiry   time.Time
	generation uint64
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config Config) *CircuitBreaker {
	cb := &CircuitBreaker{
		name:          config.Name,
		maxRequests:   config.MaxRequests,
		interval:      config.Interval,
		timeout:       config.Timeout,
		readyToTrip:   config.ReadyToTrip,
		onStateChange: config.OnStateChange,
		isSuccessful:  config.IsSuccessful,
	}

	if cb.maxRequests == 0 {
		cb.maxRequests = 1
	}
	if cb.interval <= 0 {
		cb.interval = 10 * time.Second
	}
	if cb.timeout <= 0 {
		cb.timeout = 30 * time.Second
	}
	if cb.readyToTrip == nil {
		cb.readyToTrip = func(counts Counts) bool {
			return counts.TotalFailures >= 5
		}
	}
	if cb.isSuccessful == nil {
		cb.isSuccessful = func(err error) bool {
			return err == nil
		}
	}

	cb.toNewGeneration(time.Now())
	return cb
}

// Execute runs the given function if the circuit breaker allows it
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	generation, err := cb.beforeRequest()
	if err != nil {
		return err
	}

	defer func() {
		if r := recover(); r != nil {
			cb.afterRequest(generation, false)
			panic(r)
		}
	}()

	err = fn()
	cb.afterRequest(generation, cb.isSuccessful(err))
	return err
}

// ExecuteWithFallback runs fn, or fallback if circuit is open
func (cb *CircuitBreaker) ExecuteWithFallback(ctx context.Context, fn func() error, fallback func() error) error {
	if cb.State() == StateOpen {
		return fallback()
	}
	return cb.Execute(ctx, fn)
}

func (cb *CircuitBreaker) beforeRequest() (uint64, error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	state, generation := cb.currentState(now)

	if state == StateOpen {
		return generation, ErrCircuitOpen
	}

	if state == StateHalfOpen && cb.counts.Requests >= cb.maxRequests {
		return generation, ErrTooManyCalls
	}

	cb.counts.Requests++
	return generation, nil
}

func (cb *CircuitBreaker) afterRequest(generation uint64, success bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	state, currentGeneration := cb.currentState(now)

	if generation != currentGeneration {
		return // stale call, ignore
	}

	if success {
		cb.onSuccess(state, now)
	} else {
		cb.onFailure(state, now)
	}
}

func (cb *CircuitBreaker) onSuccess(state State, now time.Time) {
	switch state {
	case StateClosed:
		cb.counts.TotalSuccesses++
		cb.counts.ConsecutiveSuccesses++
		cb.counts.ConsecutiveFailures = 0
	case StateHalfOpen:
		cb.counts.TotalSuccesses++
		cb.counts.ConsecutiveSuccesses++
		cb.counts.ConsecutiveFailures = 0
		if cb.counts.ConsecutiveSuccesses >= cb.maxRequests {
			cb.setState(StateClosed, now)
		}
	}
}

func (cb *CircuitBreaker) onFailure(state State, now time.Time) {
	switch state {
	case StateClosed:
		cb.counts.TotalFailures++
		cb.counts.ConsecutiveFailures++
		cb.counts.ConsecutiveSuccesses = 0
		if cb.readyToTrip(cb.counts) {
			cb.setState(StateOpen, now)
		}
	case StateHalfOpen:
		cb.counts.TotalFailures++
		cb.counts.ConsecutiveFailures++
		cb.counts.ConsecutiveSuccesses = 0
		cb.setState(StateOpen, now)
	}
}

func (cb *CircuitBreaker) currentState(now time.Time) (State, uint64) {
	switch cb.state {
	case StateClosed:
		if !cb.expiry.IsZero() && now.After(cb.expiry) {
			cb.toNewGeneration(now)
			return StateClosed, cb.generation
		}
		return StateClosed, cb.generation
	case StateOpen:
		if now.After(cb.expiry) {
			cb.setState(StateHalfOpen, now)
			return StateHalfOpen, cb.generation
		}
		return StateOpen, cb.generation
	case StateHalfOpen:
		return StateHalfOpen, cb.generation
	}
	return StateClosed, cb.generation
}

func (cb *CircuitBreaker) setState(state State, now time.Time) {
	if cb.state == state {
		return
	}

	prevState := cb.state
	cb.state = state

	switch state {
	case StateClosed:
		cb.counts = Counts{}
		cb.expiry = now.Add(cb.interval)
	case StateOpen:
		cb.expiry = now.Add(cb.timeout)
	case StateHalfOpen:
		cb.counts = Counts{}
		cb.expiry = now.Add(cb.interval)
	}

	if cb.onStateChange != nil {
		cb.onStateChange(cb.name, prevState, state)
	}
}

func (cb *CircuitBreaker) toNewGeneration(now time.Time) {
	cb.generation++
	cb.counts = Counts{}
	cb.expiry = now.Add(cb.interval)
}

// State returns the current state
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := time.Now()
	state, _ := cb.currentState(now)
	return state
}

// Name returns the circuit breaker name
func (cb *CircuitBreaker) Name() string {
	return cb.name
}

// Counts returns current counts
func (cb *CircuitBreaker) Counts() Counts {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.counts
}

// Reset manually resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.toNewGeneration(time.Now())
	cb.state = StateClosed
	if cb.onStateChange != nil {
		cb.onStateChange(cb.name, StateOpen, StateClosed)
	}
}
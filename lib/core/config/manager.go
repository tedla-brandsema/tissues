package config

import (
	"context"
	"fmt"
	"sync"
)

// Manager serializes complete profile reloads around an atomic Slot.
type Manager[T any] struct {
	mu      sync.Mutex
	options LoadOptions
	slot    *Slot[T]
}

func NewManager[T any](ctx context.Context, options LoadOptions) (*Manager[T], error) {
	profile, err := Load[T](ctx, options)
	if err != nil {
		return nil, err
	}
	slot, err := NewSlot(profile)
	if err != nil {
		return nil, err
	}
	return &Manager[T]{options: options, slot: slot}, nil
}

func (m *Manager[T]) Current() Profile[T] { return m.slot.Current() }

func (m *Manager[T]) Reload(ctx context.Context) (ReplaceResult[T], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate, err := Load[T](ctx, m.options)
	if err != nil {
		return ReplaceResult[T]{Profile: m.slot.Current()}, err
	}
	return m.slot.Replace(candidate)
}

func (m *Manager[T]) Clone(ctx context.Context, destination string) error {
	if m.options.Store == nil {
		return fmt.Errorf("clone requires a profile store")
	}
	return Clone(ctx, m.options.Store, m.options.Name, destination)
}

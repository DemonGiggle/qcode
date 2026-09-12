package control

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrInteractionNotFound = errors.New("interaction not found")
	ErrInteractionResolved = errors.New("interaction already resolved")
)

const resolvedInteractionHistory = 256

type InteractionKind string

const (
	InteractionDirectoryApproval InteractionKind = "directory_approval"
	InteractionQuestions         InteractionKind = "questions"
	InteractionPlanDecision      InteractionKind = "plan_decision"
)

// Interaction is a transport-neutral request for human input. Payload is kept
// opaque at this boundary so each interaction kind can evolve independently.
type Interaction struct {
	ID        string          `json:"id"`
	AgentID   string          `json:"agent_id"`
	Kind      InteractionKind `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type Resolution struct {
	InteractionID string          `json:"interaction_id"`
	Value         json.RawMessage `json:"value,omitempty"`
	ResolvedBy    string          `json:"resolved_by,omitempty"`
}

type pendingInteraction struct {
	interaction Interaction
	result      chan Resolution
}

// InteractionBroker lets local and remote controllers race safely to answer
// one human-input request. The first successful resolution wins.
type InteractionBroker struct {
	mu            sync.Mutex
	nextID        uint64
	pending       map[string]*pendingInteraction
	resolved      map[string]struct{}
	resolvedOrder []string
	onRequest     func(Interaction)
	onResolve     func(Resolution)
}

func NewInteractionBroker() *InteractionBroker {
	return &InteractionBroker{pending: make(map[string]*pendingInteraction), resolved: make(map[string]struct{})}
}

func newInteractionBroker(onRequest func(Interaction), onResolve func(Resolution)) *InteractionBroker {
	broker := NewInteractionBroker()
	broker.onRequest = onRequest
	broker.onResolve = onResolve
	return broker
}

func (b *InteractionBroker) Request(ctx context.Context, interaction Interaction) (Resolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(interaction.AgentID) == "" {
		return Resolution{}, errors.New("interaction agent ID must not be empty")
	}
	b.mu.Lock()
	b.nextID++
	interaction.ID = "interaction-" + formatUint(b.nextID)
	interaction.CreatedAt = time.Now().UTC()
	pending := &pendingInteraction{interaction: interaction, result: make(chan Resolution, 1)}
	b.pending[interaction.ID] = pending
	onRequest := b.onRequest
	b.mu.Unlock()
	if onRequest != nil {
		onRequest(interaction)
	}

	select {
	case result := <-pending.result:
		return result, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, interaction.ID)
		b.mu.Unlock()
		return Resolution{}, ctx.Err()
	}
}

func (b *InteractionBroker) Pending() []Interaction {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]Interaction, 0, len(b.pending))
	for _, pending := range b.pending {
		result = append(result, pending.interaction)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (b *InteractionBroker) Resolve(resolution Resolution) error {
	b.mu.Lock()
	pending, ok := b.pending[resolution.InteractionID]
	if !ok {
		_, resolved := b.resolved[resolution.InteractionID]
		b.mu.Unlock()
		if resolved {
			return ErrInteractionResolved
		}
		return ErrInteractionNotFound
	}
	delete(b.pending, resolution.InteractionID)
	b.resolved[resolution.InteractionID] = struct{}{}
	b.resolvedOrder = append(b.resolvedOrder, resolution.InteractionID)
	if len(b.resolvedOrder) > resolvedInteractionHistory {
		delete(b.resolved, b.resolvedOrder[0])
		b.resolvedOrder = b.resolvedOrder[1:]
	}
	onResolve := b.onResolve
	b.mu.Unlock()
	pending.result <- resolution
	if onResolve != nil {
		onResolve(resolution)
	}
	return nil
}

func formatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

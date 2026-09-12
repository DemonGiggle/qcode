package control

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"qcode/internal/session"
)

var (
	ErrInteractionNotFound = errors.New("interaction not found")
	ErrInteractionResolved = errors.New("interaction already resolved")
)

const resolvedInteractionHistory = 256

type InteractionKind = session.InteractionKind
type Interaction = session.Interaction
type Resolution = session.Resolution

const (
	InteractionDirectoryApproval = session.InteractionDirectoryApproval
	InteractionQuestions         = session.InteractionQuestions
	InteractionPlanDecision      = session.InteractionPlanDecision
	InteractionLearningApproval  = session.InteractionLearningApproval
)

type pendingInteraction struct {
	interaction Interaction
	result      chan Resolution
}

// InteractionRequest is an opened human-input request. It allows a local UI
// to present the request while a remote controller races to resolve it.
type InteractionRequest struct {
	Interaction Interaction
	broker      *InteractionBroker
	pending     *pendingInteraction
}

func (r *InteractionRequest) InteractionInfo() session.Interaction { return r.Interaction }

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
	request, err := b.Begin(interaction)
	if err != nil {
		return Resolution{}, err
	}
	return request.Wait(ctx)
}

func (b *InteractionBroker) Begin(interaction Interaction) (*InteractionRequest, error) {
	if strings.TrimSpace(interaction.AgentID) == "" {
		return nil, errors.New("interaction agent ID must not be empty")
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
	return &InteractionRequest{Interaction: interaction, broker: b, pending: pending}, nil
}

func (r *InteractionRequest) Wait(ctx context.Context) (Resolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result := <-r.pending.result:
		return result, nil
	case <-ctx.Done():
		r.broker.mu.Lock()
		if current := r.broker.pending[r.Interaction.ID]; current == r.pending {
			delete(r.broker.pending, r.Interaction.ID)
		}
		r.broker.mu.Unlock()
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

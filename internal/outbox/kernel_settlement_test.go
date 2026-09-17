package outbox

import (
	"context"
	"errors"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

type recordingKernelSettlementRepository struct {
	event workspace.OutboxEvent
	err   error
	calls int
}

func (r *recordingKernelSettlementRepository) DeliverKernelBackgroundSettlement(
	_ context.Context,
	event workspace.OutboxEvent,
) error {
	r.calls++
	r.event = event
	return r.err
}

func TestKernelSettlementDelivererDelegatesExactTopic(t *testing.T) {
	repository := &recordingKernelSettlementRepository{}
	callbackCalls := 0
	deliverer := KernelSettlementDeliverer{
		Store: repository,
		OnDelivered: func(_ context.Context, delivered workspace.OutboxEvent) error {
			callbackCalls++
			if delivered.ID != "kernel-settlement-a" {
				t.Fatalf("delivered event=%#v", delivered)
			}
			return nil
		},
	}
	event := workspace.OutboxEvent{ID: "kernel-settlement-a", Topic: workspace.KernelResultSettlementOutboxTopic}
	if err := deliverer.Deliver(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 || callbackCalls != 1 || repository.event.ID != event.ID {
		t.Fatalf("delivery calls=%d callbacks=%d event=%#v", repository.calls, callbackCalls, repository.event)
	}
}

func TestKernelSettlementDelivererRejectsWrongTopicAndPropagatesFailure(t *testing.T) {
	repository := &recordingKernelSettlementRepository{}
	deliverer := KernelSettlementDeliverer{Store: repository}
	if err := deliverer.Deliver(context.Background(), workspace.OutboxEvent{Topic: "realtime"}); err == nil {
		t.Fatal("wrong topic was accepted")
	}
	if repository.calls != 0 {
		t.Fatalf("wrong topic reached repository calls=%d", repository.calls)
	}

	repository.err = errors.New("settlement unavailable")
	err := deliverer.Deliver(context.Background(), workspace.OutboxEvent{
		Topic: workspace.KernelResultSettlementOutboxTopic,
	})
	if !errors.Is(err, repository.err) {
		t.Fatalf("delivery error=%v want=%v", err, repository.err)
	}
}

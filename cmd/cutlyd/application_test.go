package main

import (
	"context"
	"errors"
	"testing"

	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/scheduler"
)

func TestNotifyingCoordinatorWakesAfterSuccessfulDurableTransitions(t *testing.T) {
	notifier := &countingNotifier{}
	delegate := &stubQueueCoordinator{}
	coordinator := &notifyingCoordinator{dispatcher: delegate, workers: notifier}

	if _, _, err := coordinator.Submit(context.Background(), renderapi.RenderJob{}); err != nil || notifier.calls != 1 {
		t.Fatalf("submit error=%v notifications=%d", err, notifier.calls)
	}
	if _, _, err := coordinator.RequestCancel(context.Background(), "job"); err != nil || notifier.calls != 2 {
		t.Fatalf("cancel error=%v notifications=%d", err, notifier.calls)
	}

	delegate.err = errors.New("durable transition failed")
	if _, _, err := coordinator.Submit(context.Background(), renderapi.RenderJob{}); err == nil || notifier.calls != 2 {
		t.Fatalf("failed submit error=%v notifications=%d", err, notifier.calls)
	}
	if _, _, err := coordinator.RequestCancel(context.Background(), "job"); err == nil || notifier.calls != 2 {
		t.Fatalf("failed cancel error=%v notifications=%d", err, notifier.calls)
	}
}

type countingNotifier struct{ calls int }

func (notifier *countingNotifier) Notify() { notifier.calls++ }

type stubQueueCoordinator struct{ err error }

func (coordinator *stubQueueCoordinator) Submit(context.Context, renderapi.RenderJob) (scheduler.JobSnapshot, bool, error) {
	return scheduler.JobSnapshot{}, false, coordinator.err
}

func (*stubQueueCoordinator) Snapshot(string) (scheduler.JobSnapshot, error) {
	return scheduler.JobSnapshot{}, nil
}

func (coordinator *stubQueueCoordinator) RequestCancel(context.Context, string) (scheduler.JobSnapshot, bool, error) {
	return scheduler.JobSnapshot{}, false, coordinator.err
}

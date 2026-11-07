package chunkplan

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/evidence"
)

func TestAggregateVideoAndAudioReuseDecodedSequenceContract(t *testing.T) {
	job := testJob("job-aggregate", 130, 131_073)
	plan := mustPlan(t, job)
	var videoReceipts, audioReceipts []Receipt
	for _, task := range plan.Tasks {
		receipt := mustReceipt(t, task, 1)
		if task.FrameRange != nil {
			videoReceipts = append(videoReceipts, receipt)
		} else {
			audioReceipts = append(audioReceipts, receipt)
		}
	}
	video, err := AggregateVideo(context.Background(), job.FrameRange, videoReceipts)
	if err != nil {
		t.Fatal(err)
	}
	if video.Unit != evidence.EvidenceFrames || video.Domain != VideoOutputProfileV1 ||
		len(video.Chunks) != 3 || video.Chunks[2].First != 128 || video.Chunks[2].Count != 2 {
		t.Fatalf("video sequence = %+v", video)
	}
	wantVideoRoot, err := evidence.ComputeDecodedRoot(video)
	if err != nil || video.RootHash != wantVideoRoot {
		t.Fatalf("video root = %q/%q, %v", video.RootHash, wantVideoRoot, err)
	}
	audio, err := AggregateAudio(context.Background(), job.SampleRange, audioReceipts)
	if err != nil {
		t.Fatal(err)
	}
	if audio.Unit != evidence.EvidenceSamples || audio.Domain != AudioOutputProfileV1 ||
		len(audio.Chunks) != 3 || audio.Chunks[2].First != 131_072 || audio.Chunks[2].Count != 1 {
		t.Fatalf("audio sequence = %+v", audio)
	}
}

func TestAggregateRejectsGapOrderLineageDomainAndTamper(t *testing.T) {
	job := testJob("job-aggregate-fail", 65, 0)
	plan := mustPlan(t, job)
	receipts := []Receipt{mustReceipt(t, plan.Tasks[0], 1), mustReceipt(t, plan.Tasks[1], 1)}
	tests := []struct {
		name   string
		mutate func([]Receipt) []Receipt
	}{
		{name: "reordered", mutate: func(values []Receipt) []Receipt { values[0], values[1] = values[1], values[0]; return values }},
		{name: "missing", mutate: func(values []Receipt) []Receipt { return values[:1] }},
		{name: "lineage", mutate: func(values []Receipt) []Receipt {
			values[1].PlanFingerprint = digest("other")
			values[1].Fingerprint = refingerprint(t, values[1])
			return values
		}},
		{name: "domain", mutate: func(values []Receipt) []Receipt { values[1].Profile = AudioReceiptProfileV1; return values }},
		{name: "tamper", mutate: func(values []Receipt) []Receipt { values[1].Output.ContentHash = digest("other"); return values }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := cloneReceipts(receipts)
			values = test.mutate(values)
			sequence, err := AggregateVideo(context.Background(), job.FrameRange, values)
			if !errors.Is(err, ErrAggregate) || sequence.RootHash != "" || sequence.Chunks != nil {
				t.Fatalf("sequence = %+v, err = %v", sequence, err)
			}
		})
	}
}

func TestAggregateCancellationAndBoundsAreFailureZero(t *testing.T) {
	job := testJob("job-aggregate-cancel", 1, 0)
	plan := mustPlan(t, job)
	receipt := mustReceipt(t, plan.Tasks[0], 1)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	sequence, err := AggregateVideo(canceled, job.FrameRange, []Receipt{receipt})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(sequence, evidence.DecodedSequence{}) {
		t.Fatalf("canceled = %+v, %v", sequence, err)
	}
	sequence, err = AggregateVideo(context.Background(), videoedit.TickRange{}, nil)
	if !errors.Is(err, ErrAggregate) || !reflect.DeepEqual(sequence, evidence.DecodedSequence{}) {
		t.Fatalf("empty = %+v, %v", sequence, err)
	}
}

func cloneReceipts(values []Receipt) []Receipt {
	result := make([]Receipt, len(values))
	for index, value := range values {
		result[index] = cloneReceipt(value)
	}
	return result
}

func refingerprint(t *testing.T, value Receipt) string {
	t.Helper()
	result, err := receiptFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

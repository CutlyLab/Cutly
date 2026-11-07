package chunkplan

import (
	"context"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/evidence"
	"github.com/CutlyLab/Cutly/render/stagedag"
)

func AggregateVideo(ctx context.Context, expected videoedit.TickRange, receipts []Receipt) (evidence.DecodedSequence, error) {
	return aggregate(ctx, domainVideo, expected, receipts)
}

func AggregateAudio(ctx context.Context, expected videoedit.TickRange, receipts []Receipt) (evidence.DecodedSequence, error) {
	return aggregate(ctx, domainAudio, expected, receipts)
}

func aggregate(ctx context.Context, domain chunkDomain, expected videoedit.TickRange,
	receipts []Receipt) (sequence evidence.DecodedSequence, returnErr error) {
	defer func() {
		if returnErr != nil {
			sequence = evidence.DecodedSequence{}
		}
	}()
	if ctx == nil {
		return evidence.DecodedSequence{}, fmt.Errorf("%w: nil context", ErrAggregate)
	}
	if err := ctx.Err(); err != nil {
		return evidence.DecodedSequence{}, err
	}
	if err := expected.Validate(); err != nil || expected.First < 0 || expected.Count <= 0 || len(receipts) == 0 || len(receipts) > stagedag.MaxTasks {
		return evidence.DecodedSequence{}, fmt.Errorf("%w: expected range and bounded receipts are required", ErrAggregate)
	}
	profile := profileFor(domain)
	wantCount, err := chunkCount(expected.Count, profile.chunkSize)
	if err != nil || uint64(len(receipts)) != wantCount {
		return evidence.DecodedSequence{}, errors.Join(fmt.Errorf("%w: receipt count does not match fixed partition", ErrAggregate), err)
	}
	chunks := make([]evidence.DecodedChunk, 0, len(receipts))
	next := expected.First
	var planFingerprint, manifestFingerprint, outputProfile string
	var unitBytes int64
	for index, rawReceipt := range receipts {
		if index&255 == 0 {
			if err := ctx.Err(); err != nil {
				return evidence.DecodedSequence{}, err
			}
		}
		receipt := cloneReceipt(rawReceipt)
		if err := validateReceiptShape(receipt); err != nil {
			return evidence.DecodedSequence{}, errors.Join(ErrAggregate, err)
		}
		wantFingerprint, err := receiptFingerprint(receipt)
		if err != nil || receipt.Fingerprint != wantFingerprint {
			return evidence.DecodedSequence{}, errors.Join(fmt.Errorf("%w: receipt %d fingerprint", ErrAggregate, index), err)
		}
		actualDomain, _ := profileFromReceipt(receipt.Profile)
		if actualDomain != domain {
			return evidence.DecodedSequence{}, fmt.Errorf("%w: receipt %d domain", ErrAggregate, index)
		}
		wantTaskID := fmt.Sprintf("%s-%05d", profile.idPrefix, index)
		if receipt.TaskID != wantTaskID {
			return evidence.DecodedSequence{}, fmt.Errorf("%w: receipt %d task ID", ErrAggregate, index)
		}
		currentRange := receipt.FrameRange
		if domain == domainAudio {
			currentRange = receipt.SampleRange
		}
		remaining := expected.Count - int64(index)*profile.chunkSize
		wantChunkCount := profile.chunkSize
		if remaining < wantChunkCount {
			wantChunkCount = remaining
		}
		if currentRange.First != next || currentRange.Count != wantChunkCount {
			return evidence.DecodedSequence{}, fmt.Errorf("%w: receipt %d range [%d,%d) does not match fixed partition [%d,%d)",
				ErrAggregate, index, currentRange.First, currentRange.First+currentRange.Count, next, next+wantChunkCount)
		}
		end, err := currentRange.EndExclusive()
		if err != nil {
			return evidence.DecodedSequence{}, errors.Join(ErrAggregate, err)
		}
		next = end
		if index == 0 {
			planFingerprint, manifestFingerprint = receipt.PlanFingerprint, receipt.ExecutionManifestFingerprint
			outputProfile, unitBytes = receipt.OutputProfile, receipt.Output.UnitBytes
		} else if receipt.PlanFingerprint != planFingerprint ||
			receipt.ExecutionManifestFingerprint != manifestFingerprint || receipt.OutputProfile != outputProfile ||
			receipt.Output.UnitBytes != unitBytes {
			return evidence.DecodedSequence{}, fmt.Errorf("%w: receipt %d lineage or domain differs", ErrAggregate, index)
		}
		chunks = append(chunks, evidence.DecodedChunk{
			First: currentRange.First, Count: currentRange.Count, ContentHash: receipt.Output.ContentHash,
		})
	}
	expectedEnd, err := expected.EndExclusive()
	if err != nil || next != expectedEnd {
		return evidence.DecodedSequence{}, errors.Join(fmt.Errorf("%w: receipts end at %d, want %d", ErrAggregate, next, expectedEnd), err)
	}
	unit := evidence.EvidenceFrames
	if domain == domainAudio {
		unit = evidence.EvidenceSamples
	}
	sequence = evidence.DecodedSequence{
		Domain: outputProfile, Unit: unit, UnitBytes: unitBytes, Range: expected, Chunks: chunks,
	}
	root, err := evidence.ComputeDecodedRoot(sequence)
	if err != nil {
		return evidence.DecodedSequence{}, errors.Join(ErrAggregate, err)
	}
	sequence.RootHash = root
	if err := ctx.Err(); err != nil {
		return evidence.DecodedSequence{}, err
	}
	return sequence, nil
}

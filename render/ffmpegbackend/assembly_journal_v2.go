package ffmpegbackend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/evidence"
)

const journalProfileV2 = "cutly.ffmpeg.assembly-ownership.document-v2.v2"

type ownedLeafV2 struct {
	Name  string `json:"name"`
	Dev   uint64 `json:"dev"`
	Inode uint64 `json:"inode"`
}
type journalRecordV2 struct {
	Profile            string      `json:"profile"`
	Token              string      `json:"token"`
	BindingFingerprint string      `json:"binding_fingerprint"`
	Worker             string      `json:"worker"`
	Runtime            string      `json:"runtime"`
	Target             string      `json:"target"`
	Media              ownedLeafV2 `json:"media"`
	Evidence           ownedLeafV2 `json:"evidence"`
	Self               ownedLeafV2 `json:"self"`
}
type journalV2 struct {
	record      journalRecordV2
	file        *os.File
	name        string
	raw         []byte
	media, side *os.File
}

func journalNameV2(uri string) string {
	s := sha256.Sum256([]byte(uri))
	return ".cutly-owner-v2-" + hex.EncodeToString(s[:]) + ".json"
}
func (b *ChunkAssemblerV2) checkpointV2(s string) {
	if b.journalHook != nil {
		b.journalHook(s)
	}
}
func ownershipV2(name string, f *os.File) (ownedLeafV2, error) {
	i, e := f.Stat()
	if e != nil {
		return ownedLeafV2{}, e
	}
	d, n, e := inodeV2(i)
	return ownedLeafV2{Name: name, Dev: d, Inode: n}, e
}

// A journal is immutable write-ahead ownership, not a claim that multiple
// filesystem names commit atomically. No target link can precede its fsync.
func (b *ChunkAssemblerV2) newJournalV2(ctx context.Context, r evidence.BindingV2) (out *journalV2, err error) {
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, err
	}
	tag := hex.EncodeToString(token[:])
	prefix := ".cutly-owned-v2-" + tag
	j := &journalV2{name: journalNameV2(r.ArtifactURI)}
	created := false
	defer func() {
		if out == nil {
			if j.media != nil {
				err = errors.Join(err, b.removeOwnedV2(prefix+".mp4", j.media), j.media.Close())
			}
			if j.side != nil {
				err = errors.Join(err, b.removeOwnedV2(prefix+".json", j.side), j.side.Close())
			}
			if j.file != nil {
				if created {
					err = errors.Join(err, b.removeOwnedV2(j.name, j.file))
				}
				err = errors.Join(err, b.removeOwnedV2(prefix+".owner", j.file), j.file.Close())
			}
		}
	}()
	j.media, err = b.io.outputHandle.OpenFile(prefix+".mp4", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	b.checkpointV2("pre-journal-created")
	j.side, err = b.io.outputHandle.OpenFile(prefix+".json", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	j.record = journalRecordV2{Profile: journalProfileV2, Token: tag, BindingFingerprint: r.Fingerprint, Worker: b.workerDigest, Runtime: r.MediaRuntime.FFmpegBuild, Target: filepath.Base(mustArtifactPathV2(b, r.ArtifactURI))}
	j.record.Media, err = ownershipV2(prefix+".mp4", j.media)
	if err != nil {
		return nil, err
	}
	j.record.Evidence, err = ownershipV2(prefix+".json", j.side)
	if err != nil {
		return nil, err
	}
	j.file, err = b.io.outputHandle.OpenFile(prefix+".owner", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	j.record.Self, err = ownershipV2(j.name, j.file)
	if err != nil {
		return nil, err
	}
	j.raw, err = videoedit.CanonicalJSON(j.record)
	if err != nil {
		return nil, err
	}
	if err = writePublishedSnapshot(j.file, j.raw); err != nil {
		return nil, err
	}
	if err = j.file.Sync(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = linkOpenFileNoReplace(b.io.outputHandle, prefix+".owner", j.name, j.file); err != nil {
		return nil, err
	}
	created = true
	if err = b.io.syncPublicationDirectory(); err != nil {
		return nil, err
	}
	b.checkpointV2("journal-alias-durable")
	if err = b.removeOwnedV2(prefix+".owner", j.file); err != nil {
		return nil, err
	}
	if err = b.io.syncPublicationDirectory(); err != nil {
		return nil, err
	}
	out = j
	return out, nil
}
func (j *journalV2) close() error {
	var es []error
	for _, f := range []*os.File{j.media, j.side, j.file} {
		if f != nil {
			es = append(es, f.Close())
		}
	}
	return errors.Join(es...)
}

func (b *ChunkAssemblerV2) readJournalV2(ctx context.Context, r evidence.BindingV2) (j *journalV2, err error) {
	name := journalNameV2(r.ArtifactURI)
	exists, err := b.io.publishedLeafExists(name)
	if err != nil || !exists {
		return nil, err
	}
	f, err := b.io.openPublishedRegular(name, "journal")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			err = errors.Join(err, f.Close())
		}
	}()
	raw, err := readBoundedPublished(ctx, f, 16<<10)
	if err != nil {
		return nil, err
	}
	var rec journalRecordV2
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&rec); err != nil {
		return nil, err
	}
	canonical, err := videoedit.CanonicalJSON(rec)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, fmt.Errorf("%w: noncanonical ownership journal", ErrPublication)
	}
	token, err := hex.DecodeString(rec.Token)
	if err != nil || len(token) != 16 || rec.Token != strings.ToLower(rec.Token) {
		return nil, ErrPublication
	}
	prefix := ".cutly-owned-v2-" + rec.Token
	if rec.Profile != journalProfileV2 || rec.BindingFingerprint != r.Fingerprint || rec.Worker != b.workerDigest || rec.Runtime != r.MediaRuntime.FFmpegBuild || rec.Target != filepath.Base(mustArtifactPathV2(b, r.ArtifactURI)) || rec.Media.Name != prefix+".mp4" || rec.Evidence.Name != prefix+".json" || rec.Media.Inode == 0 || rec.Evidence.Inode == 0 {
		return nil, fmt.Errorf("%w: foreign ownership journal", ErrPublication)
	}
	self, err := ownershipV2(name, f)
	if err != nil {
		return nil, err
	}
	if rec.Self != self {
		return nil, fmt.Errorf("%w: replaced ownership journal", ErrPublication)
	}
	success = true
	return &journalV2{record: rec, file: f, name: name, raw: raw}, nil
}

func (b *ChunkAssemblerV2) verifyJournalV2(ctx context.Context, j *journalV2) error {
	if err := b.io.verifyPublishedName(j.name, j.file); err != nil {
		return err
	}
	raw, err := readBoundedPublished(ctx, j.file, 16<<10)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, j.raw) {
		return fmt.Errorf("%w: journal changed", ErrPublication)
	}
	return nil
}
func (b *ChunkAssemblerV2) leafMatchesV2(name string, leaf ownedLeafV2) (exists bool, err error) {
	info, err := b.io.outputHandle.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return true, fmt.Errorf("%w: unowned journal leaf", ErrPublication)
	}
	dev, ino, err := inodeV2(info)
	if err != nil {
		return true, err
	}
	if dev != leaf.Dev || ino != leaf.Inode {
		return true, fmt.Errorf("%w: foreign replacement of journal leaf %s", ErrPublication, name)
	}
	return true, nil
}

// recoverJournalV2 runs only under the directory lock. All names and the journal
// itself are checked BEFORE removing anything. Unknown/foreign leftovers remain
// fail-closed. Valid complete pairs survive; incomplete owned pairs are rolled
// back so final encoding can restart from the unchanged CAS chunks.
func (b *ChunkAssemblerV2) recoverJournalV2(ctx context.Context, v evidence.VerifiedBindingV2, r evidence.BindingV2) (returnErr error) {
	j, err := b.readJournalV2(ctx, r)
	if err != nil {
		return err
	}
	if j == nil {
		directory, e := b.io.outputHandle.Open(".")
		if e != nil {
			return e
		}
		entries, e := directory.ReadDir(-1)
		e = errors.Join(e, directory.Close())
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".cutly-owned-v2-") {
				return fmt.Errorf("%w: unknown pre-journal remnant requires host inspection", ErrPublication)
			}
		}
		return nil
	}
	defer func() { returnErr = errors.Join(returnErr, j.close()) }()
	leaves := []struct {
		name string
		leaf ownedLeafV2
	}{{j.record.Media.Name, j.record.Media}, {j.record.Evidence.Name, j.record.Evidence}, {j.record.Target, j.record.Media}, {j.record.Target + ".evidence.json", j.record.Evidence}, {".cutly-owned-v2-" + j.record.Token + ".owner", j.record.Self}}
	present := make([]bool, len(leaves))
	for i, l := range leaves {
		present[i], err = b.leafMatchesV2(l.name, l.leaf)
		if err != nil {
			return err
		}
	}
	if err = b.verifyJournalV2(ctx, j); err != nil {
		return err
	}
	complete := present[2] && present[3]
	if complete {
		media, found, e := b.openChunkAssemblyV2(ctx, v)
		if e != nil {
			return e
		}
		if !found {
			return ErrPublication
		}
		if e = media.Close(); e != nil {
			return e
		}
	}
	for i, l := range leaves {
		if !present[i] || (complete && (i == 2 || i == 3)) {
			continue
		}
		if err = b.verifyJournalV2(ctx, j); err != nil {
			return err
		}
		if _, err = b.leafMatchesV2(l.name, l.leaf); err != nil {
			return err
		}
		// Open and retain identity across final unlink to avoid trusting only a name.
		f, e := b.io.openPublishedRegular(l.name, "owned-remnant")
		if e != nil {
			return e
		}
		err = errors.Join(b.removeOwnedV2(l.name, f), f.Close())
		if err != nil {
			return err
		}
	}
	if err = b.io.syncPublicationDirectory(); err != nil {
		return err
	}
	if err = b.verifyJournalV2(ctx, j); err != nil {
		return err
	}
	if err = b.removeOwnedV2(j.name, j.file); err != nil {
		return err
	}
	return b.io.syncPublicationDirectory()
}

// finishJournalV2 is used after normal cleanup. It will not erase a changed
// journal or discard crash ownership while any staging leaf remains.
func (b *ChunkAssemblerV2) finishJournalV2(ctx context.Context, j *journalV2) error {
	for _, leaf := range []ownedLeafV2{j.record.Media, j.record.Evidence} {
		exists, err := b.leafMatchesV2(leaf.Name, leaf)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: owned staging remains", ErrPublication)
		}
	}
	if err := b.verifyJournalV2(ctx, j); err != nil {
		return err
	}
	if err := b.removeOwnedV2(j.name, j.file); err != nil {
		return err
	}
	return b.io.syncPublicationDirectory()
}

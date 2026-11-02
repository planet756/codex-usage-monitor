package rollout

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const maxArchiveBytes int64 = 8 << 30
const archiveWindow = 32 << 20

var ErrArchiveInvalid = errors.New("compressed history unreadable or exceeds limits")

type archiveInfo struct {
	Stamp  sourceStamp
	Size   int64
	Meta   logMetadata
	Header [32]byte
}

func compressedPath(path string) bool { return strings.HasSuffix(strings.ToLower(path), ".jsonl.zst") }

func plainArchivePath(path string) string {
	if compressedPath(path) {
		return path[:len(path)-4]
	}
	return path
}

type cancelReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func archiveDecoder(ctx context.Context, f *os.File, size int64) (*zstd.Decoder, error) {
	source := cancelReader{ctx, io.NewSectionReader(f, 0, size)}
	decoderReuse.Lock()
	var d *zstd.Decoder
	if n := len(decoderReuse.idle); n > 0 {
		d = decoderReuse.idle[n-1]
		decoderReuse.idle[n-1] = nil
		decoderReuse.idle = decoderReuse.idle[:n-1]
	}
	decoderReuse.Unlock()
	if d != nil {
		if e := d.Reset(source); e == nil {
			return d, nil
		}
		d.Close()
	}
	return zstd.NewReader(source, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(archiveWindow), zstd.WithDecoderMaxWindow(archiveWindow))
}

// At most two idle synchronous decoders are reused within an active operation.
// Entry points drain the pool on completion/cancellation, so no decoded history
// or source handles remain cached while the monitor is idle.
var decoderReuse struct {
	sync.Mutex
	idle []*zstd.Decoder
}

func recycleDecoder(d *zstd.Decoder) {
	if e := d.Reset(nil); e != nil {
		d.Close()
		return
	}
	decoderReuse.Lock()
	if len(decoderReuse.idle) < 2 {
		decoderReuse.idle = append(decoderReuse.idle, d)
		decoderReuse.Unlock()
		return
	}
	decoderReuse.Unlock()
	d.Close()
}
func clearDecoders() {
	decoderReuse.Lock()
	idle := decoderReuse.idle
	decoderReuse.idle = nil
	decoderReuse.Unlock()
	for _, d := range idle {
		d.Close()
	}
}

// Validate the entire compressed stream, including checksums and truncation,
// without materializing chat content in RAM or in a second plaintext file.
func inspectArchive(ctx context.Context, f *os.File) (archiveInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return archiveInfo{}, err
	}
	a := archiveInfo{Stamp: stampFile(f, info)}
	d, err := archiveDecoder(ctx, f, info.Size())
	if err != nil {
		return a, ErrArchiveInvalid
	}
	defer recycleDecoder(d)
	count := &countReader{reader: io.LimitReader(d, maxArchiveBytes+1)}
	// decodeHeader's buffering may read ahead; countReader counts those bytes too.
	a.Meta, a.Header, err = decodeHeader(ctx, count)
	if err != nil {
		return a, archiveError(ctx, err)
	}
	buf := make([]byte, 64<<10)
	_, err = io.CopyBuffer(io.Discard, cancelReader{ctx, count}, buf)
	if err != nil || count.n > maxArchiveBytes {
		return a, archiveError(ctx, ErrArchiveInvalid)
	}
	a.Size = count.n
	current, err := stampPath(f.Name())
	if err != nil || current != a.Stamp {
		return a, ErrHistoryChanged
	}
	return a, nil
}

type countReader struct {
	reader io.Reader
	n      int64
}

func (r *countReader) Read(b []byte) (int, error) {
	n, e := r.reader.Read(b)
	r.n += int64(n)
	return n, e
}

func archiveError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrHistoryChanged) {
		return err
	}
	return ErrArchiveInvalid
}

type archiveFile struct {
	*os.File
	ctx     context.Context
	archive archiveInfo
	decoder *zstd.Decoder
	offset  int64
	scratch []byte
}

type decodedInfo struct {
	os.FileInfo
	size int64
}

func (i *decodedInfo) Size() int64 { return i.size }

func openArchive(ctx context.Context, path string, known *archiveInfo) (*archiveFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	var a archiveInfo
	if known != nil && known.Size > 0 && known.Stamp.unchanged(stampFile(f, info)) {
		a = *known
	} else {
		a, err = inspectArchive(ctx, f)
		if err != nil {
			f.Close()
			return nil, err
		}
	}
	return &archiveFile{File: f, ctx: ctx, archive: a}, nil
}

func (f *archiveFile) Stat() (os.FileInfo, error) {
	i, e := f.File.Stat()
	if e != nil {
		return nil, e
	}
	return &decodedInfo{i, f.archive.Size}, nil
}
func (f *archiveFile) release() {
	if f.decoder != nil {
		recycleDecoder(f.decoder)
		f.decoder = nil
	}
	f.scratch = nil
	f.offset = 0
}
func (f *archiveFile) Close() error { f.release(); return f.File.Close() }
func (f *archiveFile) ReadAt(p []byte, at int64) (int, error) {
	if at < 0 {
		return 0, os.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.decoder == nil {
		d, err := archiveDecoder(f.ctx, f.File, f.archive.Stamp.Size)
		if err != nil {
			return 0, archiveError(f.ctx, err)
		}
		f.decoder = d
		f.offset = 0
	} else if at < f.offset {
		if err := f.decoder.Reset(cancelReader{f.ctx, io.NewSectionReader(f.File, 0, f.archive.Stamp.Size)}); err != nil {
			return 0, archiveError(f.ctx, err)
		}
		f.offset = 0
	}
	if f.scratch == nil {
		f.scratch = make([]byte, 64<<10)
	}
	for f.offset < at {
		if err := f.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := io.ReadFull(f.decoder, f.scratch[:min(int64(len(f.scratch)), at-f.offset)])
		f.offset += int64(n)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, io.EOF
			}
			return 0, archiveError(f.ctx, err)
		}
	}
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(f.decoder, p)
	f.offset += int64(n)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		err = archiveError(f.ctx, err)
	}
	return n, err
}

func (c *BoundaryCache) archive(ctx context.Context, path string) (archiveInfo, error) {
	if info, e := os.Lstat(path); e != nil || !info.Mode().IsRegular() {
		return archiveInfo{}, ErrLogUnreadable
	}
	stamp, err := stampPath(path)
	if err != nil {
		return archiveInfo{}, ErrLogUnreadable
	}
	if c != nil {
		if failed, ok := c.archiveFailures[path]; ok && failed.unchanged(stamp) {
			return archiveInfo{}, ErrArchiveInvalid
		}
		if a, ok := c.archives[path]; ok && a.Size > 0 && a.Stamp.unchanged(stamp) {
			return a, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return archiveInfo{}, ErrLogUnreadable
	}
	defer f.Close()
	a, err := inspectArchive(ctx, f)
	if err != nil {
		if c != nil && errors.Is(err, ErrArchiveInvalid) {
			if c.archiveFailures == nil || len(c.archiveFailures) >= maxHistoryCandidates {
				c.archiveFailures = make(map[string]sourceStamp)
			}
			if after, e := stampPath(path); e == nil && after == stamp {
				c.archiveFailures[path] = stamp
			}
		}
		return a, err
	}
	if c != nil {
		delete(c.archiveFailures, path)
		if c.archives == nil || len(c.archives) >= maxHistoryCandidates {
			c.archives = make(map[string]archiveInfo)
		}
		c.archives[path] = a
		c.dirty = true
	}
	return a, nil
}

// Discovery needs only identity and lineage. Full stream validation happens
// before that archive contributes accounting, avoiding a scan of every backup
// before the first usable progress snapshot. Size zero means header-only.
func (c *BoundaryCache) archiveHeader(ctx context.Context, path string) (archiveInfo, error) {
	if info, e := os.Lstat(path); e != nil || !info.Mode().IsRegular() {
		return archiveInfo{}, ErrLogUnreadable
	}
	stamp, e := stampPath(path)
	if e != nil {
		return archiveInfo{}, ErrLogUnreadable
	}
	if c != nil {
		if a, ok := c.archives[path]; ok && a.Stamp.unchanged(stamp) {
			return a, nil
		}
	}
	f, e := os.Open(path)
	if e != nil {
		return archiveInfo{}, ErrLogUnreadable
	}
	defer f.Close()
	d, e := archiveDecoder(ctx, f, stamp.Size)
	if e != nil {
		return archiveInfo{}, ErrArchiveInvalid
	}
	defer recycleDecoder(d)
	a := archiveInfo{Stamp: stamp}
	a.Meta, a.Header, e = decodeHeader(ctx, d)
	if e != nil {
		return a, archiveError(ctx, e)
	}
	current, e := stampPath(path)
	if e != nil || current != stamp {
		return a, ErrHistoryChanged
	}
	if c != nil {
		if c.archives == nil || len(c.archives) >= maxHistoryCandidates {
			c.archives = make(map[string]archiveInfo)
		}
		c.archives[path] = a
		c.dirty = true
	}
	return a, nil
}

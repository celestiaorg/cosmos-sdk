package snapshots

import (
	"io"
	"math"
	"sync"

	"cosmossdk.io/errors"
	snapshottypes "cosmossdk.io/store/snapshots/types"
	storetypes "cosmossdk.io/store/types"
)

// ChunkWriter reads an input stream, splits it into fixed-size chunks, and writes them to a
// sequence of io.ReadClosers via a channel.
//
// Close / CloseWithError may run concurrently with Write (e.g. Manager.Close aborting an
// in-flight snapshot). pipe.Write is never called while mtx is held so CloseWithError can
// unblock a writer via pipe.CloseWithError. Channel close is done under mtx so it cannot
// race with a send (sending on a closed channel panics).
type ChunkWriter struct {
	mtx         sync.Mutex
	closeChOnce sync.Once

	ch        chan<- io.ReadCloser
	pipe      *io.PipeWriter
	chunkSize uint64
	written   uint64
	closed    bool
}

// NewChunkWriter creates a new ChunkWriter. If chunkSize is 0, no chunking will be done.
func NewChunkWriter(ch chan<- io.ReadCloser, chunkSize uint64) *ChunkWriter {
	return &ChunkWriter{
		ch:        ch,
		chunkSize: chunkSize,
	}
}

// chunkLocked creates a new chunk. Caller must hold w.mtx.
func (w *ChunkWriter) chunkLocked() error {
	if w.pipe != nil {
		pipe := w.pipe
		w.pipe = nil
		w.mtx.Unlock()
		err := pipe.Close()
		w.mtx.Lock()
		if err != nil {
			return err
		}
		if w.closed {
			return errors.Wrap(storetypes.ErrLogic, "cannot write to closed ChunkWriter")
		}
	}

	if w.closed {
		return errors.Wrap(storetypes.ErrLogic, "cannot write to closed ChunkWriter")
	}

	pr, pw := io.Pipe()
	// Hold the lock across the channel send so CloseWithError cannot close the channel
	// concurrently (sending on a closed channel panics).
	w.ch <- pr
	w.pipe = pw
	w.written = 0
	return nil
}

func (w *ChunkWriter) closeChannelLocked() {
	w.closeChOnce.Do(func() {
		close(w.ch)
	})
}

// Close implements io.Closer.
func (w *ChunkWriter) Close() error {
	w.mtx.Lock()
	if w.closed {
		w.mtx.Unlock()
		return nil
	}
	w.closed = true
	pipe := w.pipe
	w.pipe = nil
	w.closeChannelLocked()
	w.mtx.Unlock()

	if pipe != nil {
		return pipe.Close()
	}
	return nil
}

// CloseWithError closes the writer and sends an error to the reader.
// Safe to call concurrently with Write.
func (w *ChunkWriter) CloseWithError(err error) {
	w.mtx.Lock()
	if w.closed {
		w.mtx.Unlock()
		return
	}
	w.closed = true

	// Historical behavior: if no chunk exists yet, create a dummy one so Save observes
	// the error instead of completing an empty snapshot successfully.
	if w.pipe == nil {
		pr, pw := io.Pipe()
		w.ch <- pr
		w.pipe = pw
	}
	pipe := w.pipe
	w.pipe = nil
	w.closeChannelLocked()
	w.mtx.Unlock()

	_ = pipe.CloseWithError(err)
}

// Write implements io.Writer.
func (w *ChunkWriter) Write(data []byte) (int, error) {
	nTotal := 0
	for len(data) > 0 {
		pipe, writeSize, err := w.prepareWrite(len(data))
		if err != nil {
			return nTotal, err
		}

		n, err := pipe.Write(data[:writeSize])
		if n > 0 {
			w.addWritten(pipe, n)
			nTotal += n
		}
		if err != nil {
			return nTotal, err
		}
		data = data[writeSize:]
	}
	return nTotal, nil
}

func (w *ChunkWriter) prepareWrite(dataLen int) (*io.PipeWriter, int, error) {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	if w.closed {
		return nil, 0, errors.Wrap(storetypes.ErrLogic, "cannot write to closed ChunkWriter")
	}

	for w.pipe == nil || (w.chunkSize > 0 && w.written >= w.chunkSize) {
		if err := w.chunkLocked(); err != nil {
			return nil, 0, err
		}
	}

	writeSize := dataLen
	if w.chunkSize > 0 {
		remaining := int(w.chunkSize - w.written)
		if writeSize > remaining {
			writeSize = remaining
		}
	}
	return w.pipe, writeSize, nil
}

func (w *ChunkWriter) addWritten(pipe *io.PipeWriter, n int) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.pipe == pipe {
		w.written += uint64(n)
	}
}

// ChunkReader reads chunks from a channel of io.ReadClosers and outputs them as an io.Reader
type ChunkReader struct {
	ch     <-chan io.ReadCloser
	reader io.ReadCloser
}

// NewChunkReader creates a new ChunkReader.
func NewChunkReader(ch <-chan io.ReadCloser) *ChunkReader {
	return &ChunkReader{ch: ch}
}

// next fetches the next chunk from the channel, or returns io.EOF if there are no more chunks.
func (r *ChunkReader) next() error {
	reader, ok := <-r.ch
	if !ok {
		return io.EOF
	}
	r.reader = reader
	return nil
}

// Close implements io.ReadCloser.
func (r *ChunkReader) Close() error {
	var err error
	if r.reader != nil {
		err = r.reader.Close()
		r.reader = nil
	}
	for reader := range r.ch {
		if e := reader.Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}

// Read implements io.Reader.
func (r *ChunkReader) Read(p []byte) (int, error) {
	if r.reader == nil {
		err := r.next()
		if err != nil {
			return 0, err
		}
	}
	n, err := r.reader.Read(p)
	if err == io.EOF {
		err = r.reader.Close()
		r.reader = nil
		if err != nil {
			return 0, err
		}
		return r.Read(p)
	}
	return n, err
}

// DrainChunks drains and closes all remaining chunks from a chunk channel.
func DrainChunks(chunks <-chan io.ReadCloser) {
	for chunk := range chunks {
		_ = chunk.Close()
	}
}

// ValidRestoreHeight will check height is valid for snapshot restore or not
func ValidRestoreHeight(format uint32, height uint64) error {
	if format != snapshottypes.CurrentFormat {
		return errors.Wrapf(snapshottypes.ErrUnknownFormat, "format %v", format)
	}

	if height == 0 {
		return errors.Wrap(storetypes.ErrLogic, "cannot restore snapshot at height 0")
	}
	if height > uint64(math.MaxInt64) {
		return errors.Wrapf(snapshottypes.ErrInvalidMetadata,
			"snapshot height %v cannot exceed %v", height, int64(math.MaxInt64))
	}

	return nil
}

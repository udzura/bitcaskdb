package codec

import (
	"bufio"
	"encoding/binary"
	"hash/crc32"
	"io"
	"time"

	"github.com/pkg/errors"

	"github.com/octu0/bitcaskdb/context"
)

// Encoder wraps an underlying io.Writer and allows you to stream
// Entry encodings on it.
type Encoder struct {
	ctx       *context.Context
	w         *bufio.Writer
	tempDir   string
	threshold int64
	closed    bool
}

func (e *Encoder) Close() {
	if e.closed {
		return
	}
	e.ctx.Buffer().BufioWriterPool().Put(e.w)
	e.closed = true
}

func (e *Encoder) Flush() error {
	if e.closed {
		return nil
	}
	return e.w.Flush()
}

func (e *Encoder) read(tempData *temporaryData, src io.Reader) (int64, uint32, error) {
	pool := e.ctx.Buffer().BytePool()
	buf := pool.Get()
	defer pool.Put(buf)

	c := crc32.New(crc32.IEEETable)
	size := int64(0)
	for {
		n, err := src.Read(buf)
		if n < 0 {
			return 0, 0, errNegativeRead
		}

		size += int64(n)
		if err == io.EOF {
			return size, c.Sum32(), nil
		}
		if err != nil {
			return 0, 0, errors.WithStack(err)
		}

		if _, err := c.Write(buf[:n]); err != nil {
			return 0, 0, errors.Wrap(err, "faield to write crc32")
		}

		if _, err := tempData.Write(buf[:n]); err != nil {
			return 0, 0, errors.Wrap(err, "failed to write temporary data")
		}
	}
}

// Encode takes any Entry and streams it to the underlying writer.
// Messages are framed with a key-length and value-length prefix.
func (e *Encoder) Encode(key []byte, r io.Reader, expiry time.Time) (int64, error) {
	if len(key) < 1 {
		return 0, errInvalidKeyOrValueSize
	}

	if r == nil {
		return e.encodeNoValue(key)
	}

	pool := e.ctx.Buffer().BufioReaderPool()
	bfr := pool.Get(r)
	defer pool.Put(bfr)

	tempData := newTemopraryData(e.ctx, e.tempDir, e.threshold)
	defer tempData.Close()

	size, checksum, err := e.read(tempData, bfr)
	if err != nil {
		return 0, errors.WithStack(err)
	}

	if size < 1 {
		return 0, errInvalidKeyOrValueSize
	}

	// keySize
	if err := binary.Write(e.w, binary.BigEndian, uint32(len(key))); err != nil {
		return 0, errors.Wrap(err, "failed writing key prefix")
	}
	// valueSize
	if err := binary.Write(e.w, binary.BigEndian, uint64(size)); err != nil {
		return 0, errors.Wrap(err, "failed writing value length prefix")
	}
	// checksumSize
	if err := binary.Write(e.w, binary.BigEndian, checksum); err != nil {
		return 0, errors.Wrap(err, "failed writing checksum data")
	}
	// ttlSize
	if expiry.IsZero() {
		if err := binary.Write(e.w, binary.BigEndian, uint64(0)); err != nil {
			return 0, errors.Wrap(err, "failed writing ttl data")
		}
	} else {
		if err := binary.Write(e.w, binary.BigEndian, uint64(expiry.Unix())); err != nil {
			return 0, errors.Wrap(err, "failed writing ttl data")
		}
	}

	if _, err := e.w.Write(key); err != nil {
		return 0, errors.Wrap(err, "failed writing key data")
	}

	if _, err := tempData.WriteTo(e.w); err != nil {
		return 0, errors.Wrap(err, "failed writing value data")
	}

	if err := e.w.Flush(); err != nil {
		return 0, errors.Wrap(err, "failed flushing data")
	}

	return int64(MetaInfoSize + int64(len(key)) + size), nil
}

func (e *Encoder) encodeNoValue(key []byte) (int64, error) {
	// keySize
	if err := binary.Write(e.w, binary.BigEndian, uint32(len(key))); err != nil {
		return 0, errors.Wrap(err, "failed writing key prefix")
	}
	// valueSize
	if err := binary.Write(e.w, binary.BigEndian, uint64(0)); err != nil {
		return 0, errors.Wrap(err, "failed writing value length prefix")
	}
	// checksumSize
	if err := binary.Write(e.w, binary.BigEndian, uint32(0)); err != nil {
		return 0, errors.Wrap(err, "failed writing checksum data")
	}
	// ttlSize
	if err := binary.Write(e.w, binary.BigEndian, uint64(0)); err != nil {
		return 0, errors.Wrap(err, "failed writing ttl data")
	}
	if _, err := e.w.Write(key); err != nil {
		return 0, errors.Wrap(err, "failed writing key data")
	}
	if err := e.w.Flush(); err != nil {
		return 0, errors.Wrap(err, "failed flushing data")
	}
	return int64(MetaInfoSize + int64(len(key))), nil
}

// NewEncoder creates a streaming Entry encoder.
func NewEncoder(ctx *context.Context, w io.Writer, tempDir string, copyTempThreshold int64) *Encoder {
	return &Encoder{
		ctx:       ctx,
		w:         ctx.Buffer().BufioWriterPool().Get(w),
		tempDir:   tempDir,
		threshold: copyTempThreshold,
		closed:    false,
	}
}
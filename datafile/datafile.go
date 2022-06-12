package datafile

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/octu0/bp"
	"github.com/pkg/errors"
	"golang.org/x/exp/mmap"

	"github.com/octu0/bitcaskdb/codec"
)

const (
	defaultDatafileFilename     = "%09d.data"
	defaultBufferSize       int = 128 * 1024
)

var (
	_ Datafile = (*datafile)(nil)
)

var (
	bytePool = bp.NewBytePool(1000, defaultBufferSize)
)

// Datafile is an interface  that represents a readable and writeable datafile
type Datafile interface {
	FileID() int32
	Name() string
	Close() error
	Sync() error
	Size() int64
	Read() (*Entry, error)
	ReadAt(index, size int64) (*Entry, error)
	Write(key []byte, value io.Reader, expiry time.Time) (int64, int64, error)
}

type datafile struct {
	sync.RWMutex

	opt    *datafileOpt
	id     int32
	r      *os.File
	ra     *mmap.ReaderAt
	w      *os.File
	offset int64
	dec    *codec.Decoder
	enc    *codec.Encoder
}

func (df *datafile) FileID() int32 {
	return df.id
}

func (df *datafile) Name() string {
	return df.r.Name()
}

func (df *datafile) Close() error {
	defer func() {
		if df.ra != nil {
			df.ra.Close()
		}
		df.r.Close()

		df.dec.Close()
		df.enc.Close()
	}()

	// Readonly datafile -- Nothing further to close on the write side
	if df.w == nil {
		return nil
	}

	if err := df.Sync(); err != nil {
		return err
	}

	return df.w.Close()
}

func (df *datafile) Sync() error {
	if df.w == nil {
		return nil
	}
	df.enc.Flush()
	return df.w.Sync()
}

func (df *datafile) Size() int64 {
	df.RLock()
	defer df.RUnlock()

	return df.offset
}

// Read reads the next entry from the datafile
func (df *datafile) Read() (*Entry, error) {
	df.Lock()
	defer df.Unlock()

	p, err := df.dec.Decode()
	if err != nil {
		return nil, err
	}

	e := &Entry{
		Key:       p.Key,
		Value:     p.Value,
		TotalSize: p.N,
		ValueSize: p.ValueSize,
		Checksum:  p.Checksum,
		Expiry:    p.Expiry,
		release: func() {
			p.Close()
		},
	}
	e.setFinalizer()
	return e, nil
}

// ReadAt the entry located at index offset with expected serialized size
func (df *datafile) ReadAt(index, size int64) (*Entry, error) {
	buf := bytePool.Get()
	defer bytePool.Put(buf)

	df.RLock()
	defer df.RUnlock()

	n := int(0)
	if df.ra != nil {
		readed, err := df.ra.ReadAt(buf[:size], index)
		if err != nil {
			return nil, err
		}
		n = readed
	} else {
		readed, err := df.r.ReadAt(buf[:size], index)
		if err != nil {
			return nil, err
		}
		n = readed
	}

	if int64(n) != size {
		return nil, errReadError
	}

	r := bytes.NewReader(buf[:size])
	d := codec.NewDecoder(df.opt.ctx, r)
	defer d.Close()

	p, err := d.Decode()
	if err != nil {
		return nil, err
	}

	e := &Entry{
		Key:       p.Key,
		Value:     p.Value,
		TotalSize: p.N,
		ValueSize: p.ValueSize,
		Checksum:  p.Checksum,
		Expiry:    p.Expiry,
		release: func() {
			p.Close()
		},
	}
	e.setFinalizer()
	return e, nil
}

func (df *datafile) Write(key []byte, value io.Reader, expiry time.Time) (int64, int64, error) {
	if df.w == nil {
		return -1, 0, errReadonly
	}

	df.Lock()
	defer df.Unlock()

	prevOffset := df.offset

	size, err := df.enc.Encode(key, value, expiry)
	if err != nil {
		return -1, 0, err
	}
	df.offset += size

	return prevOffset, size, nil
}

func OpenReadonly(funcs ...datafileOptFunc) (*datafile, error) {
	opts := append(funcs, FileMode(os.FileMode(0400)), readonly(true))
	return open(opts...)
}

func Open(funcs ...datafileOptFunc) (*datafile, error) {
	opts := append(funcs, readonly(false))
	return open(opts...)
}

// NewDatafile opens an existing datafile
func open(funcs ...datafileOptFunc) (*datafile, error) {
	opt := new(datafileOpt)
	for _, fn := range funcs {
		fn(opt)
	}

	var (
		r   *os.File
		ra  *mmap.ReaderAt
		w   *os.File
		err error
	)

	path := filepath.Join(opt.path, fmt.Sprintf(defaultDatafileFilename, opt.fileID))

	if opt.readonly != true {
		w, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, opt.fileMode)
		if err != nil {
			return nil, err
		}
	}

	r, err = os.Open(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failing open file:%s", path)
	}
	stat, err := r.Stat()
	if err != nil {
		return nil, errors.Wrap(err, "error calling Stat()")
	}

	if opt.readonly {
		ra, err = mmap.Open(path)
		if err != nil {
			return nil, errors.Wrapf(err, "mmap.Open file:%s", path)
		}
	}

	offset := stat.Size()

	dec := codec.NewDecoder(opt.ctx, r)
	enc := codec.NewEncoder(opt.ctx, w, opt.tempDir, opt.copyTempThreshold)

	return &datafile{
		opt:    opt,
		id:     opt.fileID,
		r:      r,
		ra:     ra,
		w:      w,
		offset: offset,
		dec:    dec,
		enc:    enc,
	}, nil
}
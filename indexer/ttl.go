package indexer

import (
	"os"

	"github.com/pkg/errors"
	art "github.com/plar/go-adaptive-radix-tree"

	"github.com/octu0/bitcaskdb/context"
	"github.com/octu0/bitcaskdb/util"
)

type ttlIndexer struct {
	ctx *context.Context
}

func (i *ttlIndexer) Load(path string) (art.Tree, bool, error) {
	t := art.New()
	if util.Exists(path) != true {
		return t, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return t, true, errors.WithStack(err)
	}
	defer f.Close()

	if err := readTTLIndex(i.ctx, f, t); err != nil {
		return t, true, errors.WithStack(err)
	}
	return t, true, nil
}

func (i *ttlIndexer) Save(t art.Tree, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return errors.WithStack(err)
	}

	if err := writeTTLIndex(i.ctx, f, t); err != nil {
		f.Close()
		return errors.WithStack(err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return errors.WithStack(err)
	}

	if err := f.Close(); err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func NewTTLIndexer(ctx *context.Context) *ttlIndexer {
	return &ttlIndexer{ctx}
}
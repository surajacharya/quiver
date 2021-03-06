package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"strings"

	"github.com/foursquare/gohfile"
	"github.com/foursquare/quiver/gen"
	"github.com/foursquare/quiver/util"
)

type ThriftRpcImpl struct {
	*hfile.CollectionSet
}

func (cs *ThriftRpcImpl) GetValuesSingle(req *gen.SingleHFileKeyRequest) (r *gen.SingleHFileKeyResponse, err error) {
	if Settings.debug {
		log.Printf("[GetValuesSingle] %s (%d keys)\n", *req.HfileName, len(req.SortedKeys))
	}
	hfile, err := cs.ReaderFor(*req.HfileName)
	if err != nil {
		return nil, err
	}
	reader := hfile.GetScanner()
	// TODO: clients should request strict during dev/testing?
	reader.EnforceKeyOrder = false
	defer reader.Release()

	res := new(gen.SingleHFileKeyResponse)
	res.Values = make(map[int32][]byte)
	found := int32(0)

	for idx, key := range req.SortedKeys {
		if Settings.debug {
			log.Printf("[GetValuesSingle] key: %s\n", hex.EncodeToString(key))
		}
		if idx > 0 && bytes.Equal(req.SortedKeys[idx-1], key) {
			if prev, ok := res.Values[int32(idx-1)]; ok {
				res.Values[int32(idx)] = prev
				found++
			}
			continue
		}
		value, err, ok := reader.GetFirst(key)
		if err != nil {
			return nil, err
		}
		if ok {
			found++
			if !req.GetCountOnly() {
				if req.PerKeyValueLimit != nil {
					res.Values[int32(idx)] = value[:req.GetPerKeyValueLimit()]
				} else {
					res.Values[int32(idx)] = value
				}
			}
		}
	}

	if Settings.debug {
		log.Printf("[GetValuesSingle] %s found %d of %d.\n", *req.HfileName, found, len(req.SortedKeys))
	}
	res.KeyCount = &found
	return res, nil
}

func (cs *ThriftRpcImpl) GetValuesMulti(req *gen.SingleHFileKeyRequest) (r *gen.MultiHFileKeyResponse, err error) {
	if Settings.debug {
		log.Println("[GetValuesMulti]", len(req.SortedKeys))
	}

	hfile, err := cs.ReaderFor(*req.HfileName)
	if err != nil {
		return nil, err
	}
	reader := hfile.GetScanner()
	defer reader.Release()

	res := new(gen.MultiHFileKeyResponse)
	res.Values = make(map[int32][][]byte)
	found := int32(0)

	for idx, key := range req.SortedKeys {
		values, err := reader.GetAll(key)
		if err != nil {
			return nil, err
		}
		if len(values) > 0 {
			found += int32(len(values))
			res.Values[int32(idx)] = values
		}
	}

	res.KeyCount = &found
	return res, nil

}

func (cs *ThriftRpcImpl) GetValuesForPrefixes(req *gen.PrefixRequest) (r *gen.PrefixResponse, err error) {
	res := new(gen.PrefixResponse)
	if reader, err := cs.ReaderFor(*req.HfileName); err != nil {
		return nil, err
	} else {
		i := reader.GetIterator()
		defer i.Release()
		if res.Values, err = i.AllForPrefixes(req.SortedKeys); err != nil {
			return nil, err
		} else {
			return res, nil
		}
	}
}

func (cs *ThriftRpcImpl) GetValuesMultiSplitKeys(req *gen.MultiHFileSplitKeyRequest) (r *gen.KeyToValuesResponse, err error) {
	res := make(map[string][][]byte)
	reader, err := cs.ReaderFor(*req.HfileName)
	if err != nil {
		return nil, err
	}
	scanner := reader.GetScanner()
	defer scanner.Release()

	for _, parts := range util.RevProduct(req.SplitKey) {
		// TODO(davidt): avoid allocing concated key by adding split-key search lower down.
		key := bytes.Join(parts, nil)

		if values, err := scanner.GetAll(key); err != nil {
			return nil, err
		} else if len(values) > 0 {
			res[string(key)] = values
		}
	}
	return &gen.KeyToValuesResponse{res}, nil
}

func (cs *ThriftRpcImpl) GetIterator(req *gen.IteratorRequest) (*gen.IteratorResponse, error) {
	// 	HfileName     *string `thrift:"hfileName,1" json:"hfileName"`
	// 	IncludeValues *bool   `thrift:"includeValues,2" json:"includeValues"`
	// 	LastKey       []byte  `thrift:"lastKey,3" json:"lastKey"`
	// 	SkipKeys      *int32  `thrift:"skipKeys,4" json:"skipKeys"`
	// 	ResponseLimit *int32  `thrift:"responseLimit,5" json:"responseLimit"`
	// 	EndKey        []byte  `thrift:"endKey,6" json:"endKey"`
	var err error

	if req.ResponseLimit == nil {
		return nil, fmt.Errorf("Missing limit.")
	}
	limit := int(*req.ResponseLimit)

	reader, err := cs.ReaderFor(*req.HfileName)
	if err != nil {
		return nil, err
	}
	it := reader.GetIterator()
	defer it.Release()

	remaining := false

	if req.LastKey != nil {
		remaining, err = it.Seek(req.LastKey)
	} else {
		remaining, err = it.Next()
	}

	if err != nil {
		return nil, err
	}

	res := new(gen.IteratorResponse)

	if !remaining {
		return res, nil
	}

	skipKeys := int32(0)
	lastKey := it.Key()

	if toSkip := req.GetSkipKeys(); toSkip > 0 {
		for i := int32(0); i < toSkip && remaining; i++ {
			if bytes.Equal(lastKey, it.Key()) {
				skipKeys = skipKeys + 1
			} else {
				skipKeys = 0
			}

			lastKey = it.Key()

			remaining, err = it.Next()
			if err != nil {
				return nil, err
			}
		}
		if !remaining {
			return res, nil
		}
	}

	if req.EndKey != nil {
		remaining = remaining && !hfile.After(it.Key(), req.EndKey)
	}

	r := make([]*gen.KeyValueItem, 0)
	for i := 0; i < limit && remaining; i++ {
		v := []byte{}
		if req.IncludeValues == nil || *req.IncludeValues {
			v = it.Value()
		}
		r = append(r, &gen.KeyValueItem{it.Key(), v})

		if bytes.Equal(lastKey, it.Key()) {
			skipKeys = skipKeys + 1
		} else {
			skipKeys = 1
		}
		lastKey = it.Key()

		remaining, err = it.Next()
		if err != nil {
			return nil, err
		}
		if req.EndKey != nil {
			remaining = remaining && !hfile.After(it.Key(), req.EndKey)
		}
	}
	return &gen.IteratorResponse{r, lastKey, &skipKeys}, nil
}

func GetCollectionInfo(r *hfile.Reader, keySampleSize int) (*gen.HFileInfo, error) {
	i := new(gen.HFileInfo)
	i.Name = &r.Name
	i.Path = &r.SourcePath
	c := int64(r.EntryCount)
	i.NumElements = &c
	i.FirstKey, _ = r.FirstKey()

	it := r.GetIterator()
	defer it.Release()

	if keySampleSize > 0 {
		pr := float64(keySampleSize) / float64(c)
		buf := make([][]byte, keySampleSize)
		found := 0
		next, err := it.Next()
		for next && found < keySampleSize {
			if rand.Float64() < pr {
				buf[found] = it.Key()
			}
			next, err = it.Next()
			if err != nil {
				return nil, err
			}
		}
		i.RandomKeys = buf
	}
	return i, nil
}

func (cs *ThriftRpcImpl) GetInfo(req *gen.InfoRequest) (r []*gen.HFileInfo, err error) {
	require := ""
	if req.IsSetHfileName() {
		require := req.GetHfileName()
		if require != "" && !strings.ContainsRune(require, '/') {
			require = require + "/"
		}
	}

	sample := 0
	if req.IsSetNumRandomKeys() {
		sample = int(*req.NumRandomKeys)
	}

	for name, reader := range cs.Collections {
		if require == "" || strings.HasPrefix(name, require) {
			if i, err := GetCollectionInfo(reader, sample); err != nil {
				return nil, err
			} else {
				r = append(r, i)
			}
		}
	}

	return r, nil
}

func (cs *ThriftRpcImpl) TestTimeout(waitInMillis int32) (r int32, err error) {
	return 0, fmt.Errorf("Not implemented")
}

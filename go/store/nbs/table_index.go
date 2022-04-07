// Copyright 2022 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nbs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync/atomic"

	"github.com/dolthub/dolt/go/libraries/utils/iohelp"
	"github.com/dolthub/dolt/go/store/hash"
)

var (
	ErrWrongBufferSize = errors.New("buffer length and/or capacity incorrect for chunkCount specified in footer")
	ErrWrongCopySize   = errors.New("could not copy enough bytes")
)

type tableIndex interface {
	// ChunkCount returns the total number of chunks in the indexed file.
	ChunkCount() uint32
	// EntrySuffixMatches returns true if the entry at index |idx| matches
	// the suffix of the address |h|. Used by |Lookup| after finding
	// matching indexes based on |Prefixes|.
	EntrySuffixMatches(idx uint32, h *addr) (bool, error)
	// IndexEntry returns the |indexEntry| at |idx|. Optionally puts the
	// full address of that entry in |a| if |a| is not |nil|.
	IndexEntry(idx uint32, a *addr) (indexEntry, error)
	// Lookup returns an |indexEntry| for the chunk corresponding to the
	// provided address |h|. Second returns is |true| if an entry exists
	// and |false| otherwise.
	Lookup(h *addr) (indexEntry, bool, error)
	// Ordinals returns a slice of indexes which maps the |i|th chunk in
	// the indexed file to its corresponding entry in index. The |i|th
	// entry in the result is the |i|th chunk in the indexed file, and its
	// corresponding value in the slice is the index entry that maps to it.
	Ordinals() ([]uint32, error)
	// Prefixes returns the sorted slice of |uint64| |addr| prefixes; each
	// entry corresponds to an indexed chunk address.
	Prefixes() ([]uint64, error)
	// PrefixAt returns the prefix at the specified index
	PrefixAt(idx uint32) uint64
	// TableFileSize returns the total size of the indexed table file, in bytes.
	TableFileSize() uint64
	// TotalUncompressedData returns the total uncompressed data size of
	// the table file. Used for informational statistics only.
	TotalUncompressedData() uint64

	// Close releases any resources used by this tableIndex.
	Close() error

	// Clone returns a |tableIndex| with the same contents which can be
	// |Close|d independently.
	Clone() (tableIndex, error)
}

func ReadTableFooter(rd io.ReadSeeker) (chunkCount uint32, totalUncompressedData uint64, err error) {
	footerSize := int64(magicNumberSize + uint64Size + uint32Size)
	_, err = rd.Seek(-footerSize, io.SeekEnd)

	if err != nil {
		return 0, 0, err
	}

	footer, err := iohelp.ReadNBytes(rd, int(footerSize))

	if err != nil {
		return 0, 0, err
	}

	if string(footer[uint32Size+uint64Size:]) != magicNumber {
		return 0, 0, ErrInvalidTableFile
	}

	chunkCount = binary.BigEndian.Uint32(footer)
	totalUncompressedData = binary.BigEndian.Uint64(footer[uint32Size:])

	return
}

func indexMemSize(chunkCount uint32) uint64 {
	is := indexSize(chunkCount) + footerSize
	// leftover offsets that don't fit into lengths, see NewOnHeapTableIndex
	is += uint64(offsetSize * chunkCount / 2)
	return is
}

// parses a valid nbs tableIndex from a byte stream. |buff| must end with an NBS index
// and footer and its length and capacity must match the expected indexSize for the chunkCount specified in the footer.
// Retains the buffer and does not allocate new memory except for offsets, computes on buff in place.
func parseTableIndex(buff []byte, q MemoryQuotaProvider) (onHeapTableIndex, error) {
	chunkCount, totalUncompressedData, err := ReadTableFooter(bytes.NewReader(buff))
	if err != nil {
		return onHeapTableIndex{}, err
	}
	iS := indexSize(chunkCount) + footerSize
	if uint64(len(buff)) != iS || uint64(cap(buff)) != iS {
		return onHeapTableIndex{}, ErrWrongBufferSize
	}
	buff = buff[:len(buff)-footerSize]
	return NewOnHeapTableIndex(buff, chunkCount, totalUncompressedData, q)
}

// parseTableIndexByCopy reads the footer, copies indexSize(chunkCount) bytes, and parses an on heap table index.
// Useful to create an onHeapTableIndex without retaining the entire underlying array of data.
func parseTableIndexByCopy(buff []byte, q MemoryQuotaProvider) (onHeapTableIndex, error) {
	r := bytes.NewReader(buff)
	return ReadTableIndexByCopy(r, q)
}

// ReadTableIndexByCopy loads an index into memory from an io.ReadSeeker
// Caution: Allocates new memory for entire index
func ReadTableIndexByCopy(rd io.ReadSeeker, q MemoryQuotaProvider) (onHeapTableIndex, error) {
	chunkCount, totalUncompressedData, err := ReadTableFooter(rd)
	if err != nil {
		return onHeapTableIndex{}, err
	}
	iS := int64(indexSize(chunkCount))
	_, err = rd.Seek(-(iS + footerSize), io.SeekEnd)
	if err != nil {
		return onHeapTableIndex{}, ErrInvalidTableFile
	}
	buff := make([]byte, iS)
	_, err = io.ReadFull(rd, buff)
	if err != nil {
		return onHeapTableIndex{}, err
	}

	return NewOnHeapTableIndex(buff, chunkCount, totalUncompressedData, q)
}

type onHeapTableIndex struct {
	q             MemoryQuotaProvider
	refCnt        *int32
	tableFileSize uint64
	// Tuple bytes
	tupleB []byte
	// Offset bytes
	offsetB1 []byte
	offsetB2 []byte
	// Suffix bytes
	suffixB               []byte
	chunkCount            uint32
	totalUncompressedData uint64
}

var _ tableIndex = &onHeapTableIndex{}

// NewOnHeapTableIndex creates a table index given a buffer of just the table index (no footer)
func NewOnHeapTableIndex(b []byte, chunkCount uint32, totalUncompressedData uint64, q MemoryQuotaProvider) (onHeapTableIndex, error) {
	tuples := b[:prefixTupleSize*chunkCount]
	lengths := b[prefixTupleSize*chunkCount : prefixTupleSize*chunkCount+lengthSize*chunkCount]
	suffixes := b[prefixTupleSize*chunkCount+lengthSize*chunkCount:]

	chunks2 := chunkCount / 2
	chunks1 := chunkCount - chunks2

	offsets1 := make([]byte, chunks1*offsetSize)

	lR := bytes.NewReader(lengths)
	r := NewOffsetsReader(lR)
	_, err := io.ReadFull(r, offsets1)
	if err != nil {
		return onHeapTableIndex{}, err
	}

	var offsets2 []byte
	if chunks2 > 0 {
		offsets2 = lengths[:chunks2*offsetSize]
		_, err = io.ReadFull(r, offsets2)
		if err != nil {
			return onHeapTableIndex{}, err
		}
	}

	refCnt := new(int32)
	*refCnt = 1

	return onHeapTableIndex{
		refCnt:                refCnt,
		q:                     q,
		tupleB:                tuples,
		offsetB1:              offsets1,
		offsetB2:              offsets2,
		suffixB:               suffixes,
		chunkCount:            chunkCount,
		totalUncompressedData: totalUncompressedData,
	}, nil
}

func (ti onHeapTableIndex) ChunkCount() uint32 {
	return ti.chunkCount
}

func (ti onHeapTableIndex) PrefixAt(idx uint32) uint64 {
	return ti.prefixAt(idx)
}

func (ti onHeapTableIndex) EntrySuffixMatches(idx uint32, h *addr) (bool, error) {
	ord := ti.ordinalAt(idx)
	o := ord * addrSuffixSize
	b := ti.suffixB[o : o+addrSuffixSize]
	return bytes.Equal(h[addrPrefixSize:], b), nil
}

func (ti onHeapTableIndex) IndexEntry(idx uint32, a *addr) (entry indexEntry, err error) {
	prefix, ord := ti.tupleAt(idx)

	if a != nil {
		binary.BigEndian.PutUint64(a[:], prefix)

		o := int64(addrSuffixSize * ord)
		b := ti.suffixB[o : o+addrSuffixSize]
		copy(a[addrPrefixSize:], b)
	}

	return ti.getIndexEntry(ord), nil
}

func (ti onHeapTableIndex) getIndexEntry(ord uint32) indexEntry {
	var prevOff uint64
	if ord == 0 {
		prevOff = 0
	} else {
		prevOff = ti.offsetAt(ord - 1)
	}
	ordOff := ti.offsetAt(ord)
	length := uint32(ordOff - prevOff)
	return indexResult{
		o: prevOff,
		l: length,
	}
}

func (ti onHeapTableIndex) Lookup(h *addr) (indexEntry, bool, error) {
	ord, err := ti.lookupOrdinal(h)
	if err != nil {
		return indexResult{}, false, err
	}
	if ord == ti.chunkCount {
		return indexResult{}, false, nil
	}
	return ti.getIndexEntry(ord), true, nil
}

// lookupOrdinal returns the ordinal of |h| if present. Returns |ti.chunkCount|
// if absent.
func (ti onHeapTableIndex) lookupOrdinal(h *addr) (uint32, error) {
	prefix := h.Prefix()

	for idx := ti.prefixIdx(prefix); idx < ti.chunkCount && ti.prefixAt(idx) == prefix; idx++ {
		m, err := ti.EntrySuffixMatches(idx, h)
		if err != nil {
			return ti.chunkCount, err
		}
		if m {
			return ti.ordinalAt(idx), nil
		}
	}

	return ti.chunkCount, nil
}

// prefixIdx returns the first position in |tr.prefixes| whose value ==
// |prefix|. Returns |tr.chunkCount| if absent
func (ti onHeapTableIndex) prefixIdx(prefix uint64) (idx uint32) {
	// NOTE: The golang impl of sort.Search is basically inlined here. This method can be called in
	// an extremely tight loop and inlining the code was a significant perf improvement.
	idx, j := 0, ti.chunkCount
	for idx < j {
		h := idx + (j-idx)/2 // avoid overflow when computing h
		// i ≤ h < j
		if ti.prefixAt(h) < prefix {
			idx = h + 1 // preserves f(i-1) == false
		} else {
			j = h // preserves f(j) == true
		}
	}

	return
}

func (ti onHeapTableIndex) tupleAt(idx uint32) (prefix uint64, ord uint32) {
	off := int64(prefixTupleSize * idx)
	b := ti.tupleB[off : off+prefixTupleSize]

	prefix = binary.BigEndian.Uint64(b[:])
	ord = binary.BigEndian.Uint32(b[addrPrefixSize:])
	return prefix, ord
}

func (ti onHeapTableIndex) prefixAt(idx uint32) uint64 {
	off := int64(prefixTupleSize * idx)
	b := ti.tupleB[off : off+addrPrefixSize]
	return binary.BigEndian.Uint64(b)
}

func (ti onHeapTableIndex) ordinalAt(idx uint32) uint32 {
	off := int64(prefixTupleSize*idx) + addrPrefixSize
	b := ti.tupleB[off : off+ordinalSize]
	return binary.BigEndian.Uint32(b)
}

func (ti onHeapTableIndex) offsetAt(ord uint32) uint64 {
	chunks1 := ti.chunkCount - ti.chunkCount/2
	var b []byte
	if ord < chunks1 {
		off := int64(offsetSize * ord)
		b = ti.offsetB1[off : off+offsetSize]
	} else {
		off := int64(offsetSize * (ord - chunks1))
		b = ti.offsetB2[off : off+offsetSize]
	}

	return binary.BigEndian.Uint64(b)
}

func (ti onHeapTableIndex) Ordinals() ([]uint32, error) {
	o := make([]uint32, ti.chunkCount)
	for i, off := uint32(0), 0; i < ti.chunkCount; i, off = i+1, off+prefixTupleSize {
		b := ti.tupleB[off+addrPrefixSize : off+prefixTupleSize]
		o[i] = binary.BigEndian.Uint32(b)
	}
	return o, nil
}

func (ti onHeapTableIndex) Prefixes() ([]uint64, error) {
	p := make([]uint64, ti.chunkCount)
	for i, off := uint32(0), 0; i < ti.chunkCount; i, off = i+1, off+prefixTupleSize {
		b := ti.tupleB[off : off+addrPrefixSize]
		p[i] = binary.BigEndian.Uint64(b)
	}
	return p, nil
}

func (ti onHeapTableIndex) hashAt(idx uint32) hash.Hash {
	// Get tuple
	off := int64(prefixTupleSize * idx)
	tuple := ti.tupleB[off : off+prefixTupleSize]

	// Get prefix, ordinal, and suffix
	prefix := tuple[:addrPrefixSize]
	ord := binary.BigEndian.Uint32(tuple[addrPrefixSize:]) * addrSuffixSize
	suffix := ti.suffixB[ord : ord+addrSuffixSize] // suffix is 12 bytes

	// Combine prefix and suffix to get hash
	buf := [hash.ByteLen]byte{}
	copy(buf[:addrPrefixSize], prefix)
	copy(buf[addrPrefixSize:], suffix)

	return buf
}

// prefixIdxLBound returns the first position in |tr.prefixes| whose value is <= |prefix|.
// will return index less than where prefix would be if prefix is not found.
func (ti onHeapTableIndex) prefixIdxLBound(prefix uint64) uint32 {
	l, r := uint32(0), ti.chunkCount
	for l < r {
		m := l + (r-l)/2 // find middle, rounding down
		if ti.prefixAt(m) < prefix {
			l = m + 1
		} else {
			r = m
		}
	}

	return l
}

// prefixIdxLBound returns the first position in |tr.prefixes| whose value is >= |prefix|.
// will return index greater than where prefix would be if prefix is not found.
func (ti onHeapTableIndex) prefixIdxUBound(prefix uint64) (idx uint32) {
	l, r := uint32(0), ti.chunkCount
	for l < r {
		m := l + (r-l+1)/2      // find middle, rounding up
		if m >= ti.chunkCount { // prevent index out of bounds
			return r
		}
		pre := ti.prefixAt(m)
		if pre <= prefix {
			l = m
		} else {
			r = m - 1
		}
	}

	return l
}

func (ti onHeapTableIndex) padStringAndDecode(s string, p string) uint64 {
	// Pad string
	if p == "0" {
		for i := len(s); i < 16; i++ {
			s = s + p
		}
	} else {
		for i := len(s); i < 16; i++ {
			s = p + s
		}
	}

	// Decode
	h, _ := encoding.DecodeString(s)
	return binary.BigEndian.Uint64(h)
}

func (ti onHeapTableIndex) ResolveShortHash(short []byte) ([]string, error) {
	// Convert to string
	shortHash := string(short)

	// Calculate length
	sLen := len(shortHash)

	// Find lower and upper bounds of prefix indexes to check
	var pIdxL, pIdxU uint32
	if sLen >= 13 {
		// Convert short string to prefix
		sPrefix := ti.padStringAndDecode(shortHash, "0")

		// Binary Search for prefix
		pIdxL = ti.prefixIdx(sPrefix)

		// Prefix doesn't exist
		if pIdxL == ti.chunkCount {
			return []string{}, errors.New("can't find prefix")
		}

		// Find last equal
		pIdxU = pIdxL + 1
		for sPrefix == ti.prefixAt(pIdxU) {
			pIdxU++
		}
	} else {
		// Convert short string to lower and upper bounds
		sPrefixL := ti.padStringAndDecode(shortHash, "0")
		sPrefixU := ti.padStringAndDecode(shortHash, "v")

		// Binary search for lower and upper bounds
		pIdxL = ti.prefixIdxLBound(sPrefixL)
		pIdxU = ti.prefixIdxUBound(sPrefixU)
	}

	// Go through all equal prefixes
	var res []string
	for i := pIdxL; i < pIdxU; i++ {
		// Get full hash at index
		h := ti.hashAt(i)

		// Convert to string representation
		hashStr := h.String()

		// If it matches append to result
		if hashStr[:sLen] == shortHash {
			res = append(res, hashStr)
		}
	}

	return res, nil
}

// TableFileSize returns the size of the table file that this index references.
// This assumes that the index follows immediately after the last chunk in the
// file and that the last chunk in the file is in the index.
func (ti onHeapTableIndex) TableFileSize() uint64 {
	if ti.chunkCount == 0 {
		return footerSize
	}
	entry := ti.getIndexEntry(ti.chunkCount - 1)
	offset, len := entry.Offset(), uint64(entry.Length())
	return offset + len + indexSize(ti.chunkCount) + footerSize
}

func (ti onHeapTableIndex) TotalUncompressedData() uint64 {
	return ti.totalUncompressedData
}

func (ti onHeapTableIndex) Close() error {
	cnt := atomic.AddInt32(ti.refCnt, -1)
	if cnt == 0 {
		return ti.q.ReleaseQuota(indexMemSize(ti.chunkCount))
	}
	if cnt < 0 {
		panic("Close() called and reduced ref count to < 0.")
	}

	ti.tupleB = nil
	ti.offsetB1 = nil
	ti.offsetB2 = nil
	ti.suffixB = nil

	return nil
}

func (ti onHeapTableIndex) Clone() (tableIndex, error) {
	cnt := atomic.AddInt32(ti.refCnt, 1)
	if cnt == 1 {
		panic("Clone() called after last Close(). This index is no longer valid.")
	}
	return ti, nil
}

// Package hll counts distinct visitors with sketches that can be merged:
// the visitors of a week are the merge of its hourly sketches, without
// keeping who they were. (Hourly counts cannot simply be added up: the
// same visitor would count once per hour.)
//
// A sketch keeps up to 4096 visitors exactly, as 32-bit hashes of their
// ids (4 bytes each, so a page with a few visitors an hour stores a few
// bytes). Beyond that it turns into a HyperLogLog with 2^14 registers:
// 16 KiB, about 0.8 % standard error, whatever the number of visitors.
package hll

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"math/bits"
	"slices"
)

const (
	precision = 14
	registers = 1 << precision
	// sparseMax is how many hashes a sketch keeps before it turns dense;
	// both forms then take 16 KiB.
	sparseMax = registers / 4
	// maxRank is the largest register value: 18 hash bits after the index, plus one.
	maxRank = 32 - precision + 1

	formatSparse = 1
	formatDense  = 2
)

// Sketch is a set of visitors, exact while small. The zero value is not
// usable; call New.
type Sketch struct {
	sparse map[uint32]struct{} // nil once dense
	dense  []uint8             // registers
}

// New returns an empty sketch.
func New() *Sketch {
	return &Sketch{sparse: map[uint32]struct{}{}}
}

// Hash is the 32-bit hash a sketch keeps of a visitor id. It is the same
// in every process, so sketches stored by one run merge with the next.
func Hash(id string) uint32 {
	h := fnv.New64a()
	h.Write([]byte(id))
	x := h.Sum64()
	// murmur3's finalizer spreads FNV's bits over the whole word.
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return uint32(x >> 32)
}

// Add adds a visitor.
func (s *Sketch) Add(id string) {
	s.addHash(Hash(id))
}

func (s *Sketch) addHash(h uint32) {
	if s.dense != nil {
		s.setRegister(h)
		return
	}
	s.sparse[h] = struct{}{}
	if len(s.sparse) > sparseMax {
		s.toDense()
	}
}

func (s *Sketch) setRegister(h uint32) {
	index := h >> (32 - precision)
	rank := uint8(maxRank - bits.Len32(h&(1<<(32-precision)-1)))
	if rank > s.dense[index] {
		s.dense[index] = rank
	}
}

func (s *Sketch) toDense() {
	s.dense = make([]uint8, registers)
	for h := range s.sparse {
		s.setRegister(h)
	}
	s.sparse = nil
}

// Merge adds the visitors of o.
func (s *Sketch) Merge(o *Sketch) {
	if o.dense == nil {
		for h := range o.sparse {
			s.addHash(h)
		}
		return
	}
	if s.dense == nil {
		s.toDense()
	}
	for i, r := range o.dense {
		if r > s.dense[i] {
			s.dense[i] = r
		}
	}
}

// MergeEncoded adds the visitors of an encoded sketch without decoding it
// into a Sketch first; queries that merge thousands of hourly sketches
// use it.
func (s *Sketch) MergeEncoded(data []byte) error {
	if err := check(data); err != nil {
		return err
	}
	body := data[1:]
	if data[0] == formatSparse {
		for i := 0; i < len(body); i += 4 {
			s.addHash(binary.BigEndian.Uint32(body[i:]))
		}
		return nil
	}
	if s.dense == nil {
		s.toDense()
	}
	for i, r := range body {
		if r > s.dense[i] {
			s.dense[i] = r
		}
	}
	return nil
}

// Count returns the number of distinct visitors: exact up to 4096,
// estimated beyond.
func (s *Sketch) Count() int {
	if s.dense == nil {
		return len(s.sparse)
	}
	m := float64(registers)
	sum, zeros := 0.0, 0
	for _, r := range s.dense {
		sum += math.Ldexp(1, -int(r))
		if r == 0 {
			zeros++
		}
	}
	alpha := 0.7213 / (1 + 1.079/m)
	estimate := alpha * m * m / sum
	switch {
	case estimate <= 2.5*m && zeros > 0:
		estimate = m * math.Log(m/float64(zeros)) // linear counting, better for small sets
	case estimate > math.Exp2(32)/30:
		estimate = -math.Exp2(32) * math.Log(1-estimate/math.Exp2(32))
	}
	return int(math.Round(estimate))
}

// Encode returns the sketch as bytes for the database: a format byte,
// then the sorted hashes (4 bytes each) or the 16 384 registers.
func (s *Sketch) Encode() []byte {
	if s.dense != nil {
		return append([]byte{formatDense}, s.dense...)
	}
	hashes := make([]uint32, 0, len(s.sparse))
	for h := range s.sparse {
		hashes = append(hashes, h)
	}
	slices.Sort(hashes)
	out := make([]byte, 1, 1+4*len(hashes))
	out[0] = formatSparse
	for _, h := range hashes {
		out = binary.BigEndian.AppendUint32(out, h)
	}
	return out
}

// Decode reads a sketch written by Encode.
func Decode(data []byte) (*Sketch, error) {
	s := New()
	if err := s.MergeEncoded(data); err != nil {
		return nil, err
	}
	return s, nil
}

var errFormat = errors.New("not an encoded visitor sketch")

// check reports whether data is a well-formed encoded sketch.
func check(data []byte) error {
	if len(data) == 0 {
		return errFormat
	}
	body := data[1:]
	switch data[0] {
	case formatSparse:
		if len(body)%4 != 0 {
			return errFormat
		}
	case formatDense:
		if len(body) != registers {
			return errFormat
		}
		for _, r := range body {
			if r > maxRank {
				return errFormat
			}
		}
	default:
		return errFormat
	}
	return nil
}

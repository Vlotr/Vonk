package hll

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func TestHLL_MergeAccuracy(t *testing.T) {
	// 10 000 visitors over a week: each comes back in one to three random
	// hours, so the hourly sketches overlap the way real visitors do.
	rng := rand.New(rand.NewPCG(1, 2))
	hours := make([]*Sketch, 168)
	for i := range hours {
		hours[i] = New()
	}
	for v := range 10_000 {
		id := fmt.Sprintf("visitor-%d", v)
		for range 1 + rng.IntN(3) {
			hours[rng.IntN(len(hours))].Add(id)
		}
	}

	// As the dashboard does: decode the stored sketches and merge them.
	week := New()
	for _, h := range hours {
		stored, err := Decode(h.Encode())
		if err != nil {
			t.Fatal(err)
		}
		week.Merge(stored)
	}
	got := week.Count()
	if diff := math.Abs(float64(got)-10_000) / 10_000; diff > 0.02 {
		t.Errorf("visitors over 168 hours = %d, want 10 000 within 2 %% (off by %.2f %%)", got, diff*100)
	}
}

func TestHLL_ExactForSmallSets(t *testing.T) {
	s := New()
	if s.Count() != 0 {
		t.Errorf("empty sketch counts %d", s.Count())
	}
	for i := range 500 {
		s.Add(fmt.Sprintf("user-%d", i))
		s.Add(fmt.Sprintf("user-%d", i)) // the same visitor twice is one
	}
	if got := s.Count(); got != 500 {
		t.Errorf("500 visitors counted as %d", got)
	}
	other := New()
	for i := 400; i < 700; i++ {
		other.Add(fmt.Sprintf("user-%d", i))
	}
	s.Merge(other)
	if got := s.Count(); got != 700 {
		t.Errorf("merged 500 and 300 with 100 shared = %d, want 700", got)
	}
}

func TestHLL_SmallSketchesAreSmall(t *testing.T) {
	// An hour of one page usually has a handful of visitors.
	s := New()
	for i := range 200 {
		s.Add(fmt.Sprintf("v%d", i))
	}
	if n := len(s.Encode()); n > 1024 {
		t.Errorf("200 visitors take %d bytes, want at most 1 KiB", n)
	}
	// A busy one stays bounded.
	for i := range 100_000 {
		s.Add(fmt.Sprintf("busy-%d", i))
	}
	if n := len(s.Encode()); n > 16*1024+16 {
		t.Errorf("100 000 visitors take %d bytes, want about 16 KiB at most", n)
	}
	if got := s.Count(); math.Abs(float64(got)-100_200)/100_200 > 0.03 {
		t.Errorf("100 200 visitors counted as %d", got)
	}
}

func TestHLL_DecodeRefusesGarbage(t *testing.T) {
	for _, data := range [][]byte{nil, {}, {9}, {1, 0, 0}, {2, 1, 2, 3}} {
		if _, err := Decode(data); err == nil {
			t.Errorf("Decode(%v) = nil error", data)
		}
	}
	// An empty sketch round-trips.
	s, err := Decode(New().Encode())
	if err != nil || s.Count() != 0 {
		t.Errorf("empty round trip = %v, %v", s, err)
	}
}

package autodl

import (
	"fmt"
	"path/filepath"
	"testing"
)

var rangeBenchmarkSink []int

func BenchmarkBatchRangeIDs(b *testing.B) {
	for _, count := range []int{10000, 100000, 1000000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			start, end := 1, count+1
			job := &Job{StartComment: &start, EndComment: &end}
			b.ReportAllocs()
			for b.Loop() {
				rangeBenchmarkSink = rangeIDs(job)
			}
		})
	}
}

// BenchmarkBatchLegacyRangeSetup preserves the materialized range + iterator
// setup as a comparison for BenchmarkBatchRangeIterator's bounded cursor.
func BenchmarkBatchLegacyRangeSetup(b *testing.B) {
	for _, count := range []int{10000, 100000, 1000000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			start, end := 1, count+1
			job := &Job{StartComment: &start, EndComment: &end}
			b.ReportAllocs()
			for b.Loop() {
				it, err := newIter(nil, nil, nil, "downloads", rangeIDs(job), &iterOptions{template: "{{ .DialogID }}_{{ .MessageID }}_{{ .FileName }}"})
				if err != nil {
					b.Fatal(err)
				}
				rangeBenchmarkSink = it.ids
			}
		})
	}
}

func BenchmarkBatchStateSave(b *testing.B) {
	for _, count := range []int{10000, 100000, 1000000} {
		for _, dirty := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/dirty=%t", count, dirty), func(b *testing.B) {
				s := NewState()
				ids := make([]int, count)
				for i := range ids {
					ids[i] = i + 1
				}
				s.Finish(ids...)
				path := filepath.Join(b.TempDir(), "state.json")
				if err := s.Save(path); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				var generation int64
				for b.Loop() {
					if dirty {
						generation++
						s.SetLastTS(generation)
					}
					if err := s.Save(path); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

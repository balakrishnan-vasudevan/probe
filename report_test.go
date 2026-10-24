package main

import (
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	// 1ms..100ms: p50 is the 50th value, p90 the 90th, p99 the 99th.
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	for _, c := range []struct {
		p    float64
		want time.Duration
	}{{50, 50 * time.Millisecond}, {90, 90 * time.Millisecond}, {99, 99 * time.Millisecond}, {100, 100 * time.Millisecond}} {
		if got := percentile(d, c.p); got != c.want {
			t.Errorf("p%v: got %v, want %v", c.p, got, c.want)
		}
	}
}

// The whole point of the tool: one slow outlier drags the average far
// away from what a typical request experiences, while p50 stays put.
func TestAverageVsMedianWithOutlier(t *testing.T) {
	d := []time.Duration{}
	for i := 0; i < 99; i++ {
		d = append(d, 100*time.Millisecond)
	}
	d = append(d, 10*time.Second)

	st := summarize(d)
	if st.P50 != 100*time.Millisecond {
		t.Errorf("p50 = %v, want 100ms", st.P50)
	}
	if st.Avg != 199*time.Millisecond {
		t.Errorf("avg = %v, want 199ms (one 10s request nearly doubles it)", st.Avg)
	}
	if st.Max != 10*time.Second {
		t.Errorf("max = %v, want 10s", st.Max)
	}
}

func TestSummarizeDoesNotReorderInput(t *testing.T) {
	d := []time.Duration{3, 1, 2}
	summarize(d)
	if d[0] != 3 || d[1] != 1 || d[2] != 2 {
		t.Errorf("summarize sorted the caller's slice: %v", d)
	}
}

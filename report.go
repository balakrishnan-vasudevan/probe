package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// report prints a per-phase summary, a histogram of total time, and a
// breakdown by Cloudflare cache status.
func report(w io.Writer, samples []Sample) {
	var ok []Sample
	for _, s := range samples {
		if s.Err == nil {
			ok = append(ok, s)
		}
	}
	if failed := len(samples) - len(ok); failed > 0 {
		fmt.Fprintf(w, "%d of %d requests failed and are left out below\n\n", failed, len(samples))
	}
	if len(ok) == 0 {
		fmt.Fprintln(w, "no successful requests")
		return
	}

	phases := []struct {
		name string
		get  func(Sample) time.Duration
	}{
		{"dns", func(s Sample) time.Duration { return s.DNS }},
		{"connect", func(s Sample) time.Duration { return s.Connect }},
		{"tls", func(s Sample) time.Duration { return s.TLS }},
		{"wait", func(s Sample) time.Duration { return s.Wait }},
		{"transfer", func(s Sample) time.Duration { return s.Transfer }},
		{"total", func(s Sample) time.Duration { return s.Total }},
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "phase\tmin\tavg\tp50\tp90\tp99\tmax\t")
	for _, p := range phases {
		d := make([]time.Duration, len(ok))
		for i, s := range ok {
			d[i] = p.get(s)
		}
		st := summarize(d)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n",
			p.name, ms(st.Min), ms(st.Avg), ms(st.P50), ms(st.P90), ms(st.P99), ms(st.Max))
	}
	tw.Flush()

	totals := make([]time.Duration, len(ok))
	for i, s := range ok {
		totals[i] = s.Total
	}
	fmt.Fprintln(w, "\ntotal time histogram")
	histogram(w, totals, 20, 40)

	// Group by Cloudflare cache status. Different groups are a common
	// cause of a histogram with more than one hump.
	groups := map[string][]time.Duration{}
	for _, s := range ok {
		key := s.Cache
		if key == "" {
			key = "(none)"
		}
		groups[key] = append(groups[key], s.Total)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintln(w, "\nby cf-cache-status")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "status\tcount\tavg\tp50\tp99\t")
	for _, k := range keys {
		st := summarize(groups[k])
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t\n", k, len(groups[k]), ms(st.Avg), ms(st.P50), ms(st.P99))
	}
	tw.Flush()
}

// Stats summarizes a set of durations.
type Stats struct {
	Min, Avg, P50, P90, P99, Max time.Duration
}

func summarize(d []time.Duration) Stats {
	if len(d) == 0 {
		return Stats{}
	}
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, v := range sorted {
		sum += v
	}
	return Stats{
		Min: sorted[0],
		Avg: sum / time.Duration(len(sorted)),
		P50: percentile(sorted, 50),
		P90: percentile(sorted, 90),
		P99: percentile(sorted, 99),
		Max: sorted[len(sorted)-1],
	}
}

// percentile uses the nearest-rank method on already-sorted data: the
// smallest value that at least p% of samples are less than or equal to.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p/100*float64(len(sorted)) + 0.999999) // ceil
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// histogram draws equal-width buckets from min to max as rows of '#',
// and marks which buckets contain the average, p50 and p99.
func histogram(w io.Writer, d []time.Duration, buckets, width int) {
	st := summarize(d)
	span := st.Max - st.Min
	if span == 0 {
		fmt.Fprintf(w, "  all %d samples took %s\n", len(d), ms(st.Min))
		return
	}
	size := span / time.Duration(buckets)
	if size == 0 {
		size = 1
	}
	bucketOf := func(v time.Duration) int {
		i := int((v - st.Min) / size)
		if i >= buckets {
			i = buckets - 1
		}
		return i
	}

	counts := make([]int, buckets)
	for _, v := range d {
		counts[bucketOf(v)]++
	}
	maxCount := 0
	for _, c := range counts {
		maxCount = max(maxCount, c)
	}

	marks := map[int][]string{}
	marks[bucketOf(st.Avg)] = append(marks[bucketOf(st.Avg)], "avg")
	marks[bucketOf(st.P50)] = append(marks[bucketOf(st.P50)], "p50")
	marks[bucketOf(st.P99)] = append(marks[bucketOf(st.P99)], "p99")

	for i, c := range counts {
		lo := st.Min + time.Duration(i)*size
		bar := strings.Repeat("#", (c*width+maxCount-1)/maxCount)
		label := ""
		if m := marks[i]; len(m) > 0 {
			label = "  ← " + strings.Join(m, ", ")
		}
		fmt.Fprintf(w, "  %9s │%-*s %4d%s\n", ms(lo), width, bar, c, label)
	}
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64) + "ms"
}

func writeCSV(path string, samples []Sample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	w.Write([]string{"start", "dns_ms", "connect_ms", "tls_ms", "wait_ms", "transfer_ms", "total_ms", "reused", "status", "cf_cache_status", "error"})
	f64 := func(d time.Duration) string {
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 3, 64)
	}
	for _, s := range samples {
		errText := ""
		if s.Err != nil {
			errText = s.Err.Error()
		}
		w.Write([]string{
			s.Start.UTC().Format(time.RFC3339Nano),
			f64(s.DNS), f64(s.Connect), f64(s.TLS), f64(s.Wait), f64(s.Transfer), f64(s.Total),
			strconv.FormatBool(s.Reused), strconv.Itoa(s.Status), s.Cache, errText,
		})
	}
	w.Flush()
	return w.Error()
}

package run

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestAppendWithLock_concurrent goroutines spawning N writers in parallel
// must not produce interleaved or corrupt JSONL lines. With flock the
// kernel serializes the writes; without it, large lines could partially
// interleave on POSIX append.
func TestAppendWithLock_concurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	const writers = 8
	const perWriter = 50

	// Use a long payload so writes exceed PIPE_BUF (typically 4KB) and
	// would interleave without an explicit lock.
	pad := make([]byte, 6000)
	for i := range pad {
		pad[i] = 'x'
	}
	type row struct {
		Worker  int    `json:"worker"`
		Seq     int    `json:"seq"`
		Padding string `json:"padding"`
	}

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for s := range perWriter {
				b, _ := json.Marshal(row{Worker: worker, Seq: s, Padding: string(pad)})
				b = append(b, '\n')
				if err := appendWithLock(path, b); err != nil {
					t.Errorf("appendWithLock: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Every line must round-trip through json.Unmarshal — proof there's no
	// interleaving.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	count := 0
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line %d failed to parse: %v\nfirst 200 bytes: %q", count+1, err, sc.Text()[:min(len(sc.Text()), 200)])
		}
		count++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if want := writers * perWriter; count != want {
		t.Fatalf("got %d lines, want %d", count, want)
	}
}

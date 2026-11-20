package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/host"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// floodEnv marks the flooding helper process. It stands in for a build log:
// output never stops on its own, and input must still get through.
const floodEnv = "FLT_TEST_HELPER_FLOOD"

// TestHelperFloodAgent is the body of the flooding agent the latency test
// spawns. One loop owns stdout: while it handles an input line the flood is
// paused, so the echo is a distinct event the test can time.
func TestHelperFloodAgent(t *testing.T) {
	if os.Getenv(floodEnv) != "1" {
		t.Skip("helper process only")
	}

	fmt.Printf("ready pid=%d\n", os.Getpid())

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	chunk := []byte(strings.Repeat("x", 4095) + "\n")
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return
			}
			fmt.Printf("echo: %s\n", line)
		default:
			if _, err := os.Stdout.Write(chunk); err != nil {
				return
			}
		}
	}
}

// tailLimit caps the output kept for echo matching. Matching runs in the data
// callback, so the needle is seen in the frame that carries it; the cap only
// bounds memory.
const tailLimit = 64 * 1024

// TestInputStaysFastUnderAFlood is the W7-8 acceptance check: with at least
// 2 MB/s of output crossing the whole relay (client → daemon → host → PTY →
// agent and back), twenty typed lines must echo with p95 ≤ 100ms and no sample
// above 500ms.
func TestInputStaysFastUnderAFlood(t *testing.T) {
	dir := t.TempDir()
	const token = "shared"

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	h, err := host.Start(context.Background(), host.Config{
		ID:       "sess-flood",
		Token:    token,
		StateDir: dir,
		Command:  exe,
		Args:     []string{"-test.run=TestHelperFloodAgent", "-test.v=false"},
		Env:      append(os.Environ(), floodEnv+"=1"),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("start the flooding host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	if _, err := waitRecord(t, session.NewRegistry(dir), "sess-flood"); err != nil {
		t.Fatalf("the flooding host never published a record: %v", err)
	}
	runDaemon(t, dir, token)

	var mu sync.Mutex
	var total int64
	var tail []byte
	var pending []byte
	var echoedAt time.Time
	client := dialDaemon(t, dir, token, nil, func(_ *ipc.Conn, stream uint32, payload []byte) {
		if stream != session.StreamOutput {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		total += int64(len(payload))
		tail = append(tail, payload...)
		if len(tail) > tailLimit {
			tail = append([]byte(nil), tail[len(tail)-tailLimit:]...)
		}
		// Match here, not in a poll: one coalesced flood frame can replace the
		// whole tail between polls and evict the echo before a poll sees it.
		if pending != nil && echoedAt.IsZero() && bytes.Contains(tail, pending) {
			echoedAt = time.Now()
		}
	})

	callCtx, callCancel := context.WithTimeout(context.Background(), waitFor)
	defer callCancel()
	if _, err := client.Call(callCtx, session.MethodAttach, session.AttachRequest{ID: "sess-flood"}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	count := func() int64 {
		mu.Lock()
		defer mu.Unlock()
		return total
	}

	// The rate is part of the criterion: a trickle would pass the latency
	// bound without proving anything about a flood.
	deadline := time.Now().Add(waitFor)
	for count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the flooding agent never produced output")
		}
		time.Sleep(5 * time.Millisecond)
	}
	base := count()
	windowStart := time.Now()
	time.Sleep(1 * time.Second)
	rate := float64(count()-base) / time.Since(windowStart).Seconds()
	if rate < 2*1024*1024 {
		t.Fatalf("the flood reached %.2f MB/s, below the 2 MB/s the criterion demands", rate/(1024*1024))
	}

	const samples = 20
	latencies := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		mu.Lock()
		pending = []byte(fmt.Sprintf("echo: ping-%02d", i))
		echoedAt = time.Time{}
		mu.Unlock()
		sent := time.Now()
		if err := client.SendData(session.StreamInput, []byte(fmt.Sprintf("ping-%02d\r\n", i))); err != nil {
			t.Fatalf("send input %d: %v", i, err)
		}
		echoDeadline := time.Now().Add(5 * time.Second)
		var at time.Time
		for {
			mu.Lock()
			at = echoedAt
			mu.Unlock()
			if !at.IsZero() {
				break
			}
			if time.Now().After(echoDeadline) {
				t.Fatalf("sample %d never echoed through the flood", i)
			}
			time.Sleep(time.Millisecond)
		}
		latencies = append(latencies, at.Sub(sent))
		// Let the flood rebuild between samples so every input crosses a
		// saturated pipe, not the calm after the previous echo.
		time.Sleep(150 * time.Millisecond)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[samples*95/100-1]
	worst := latencies[samples-1]
	t.Logf("flood %.2f MB/s; input latency p95=%v max=%v; samples=%v", rate/(1024*1024), p95, worst, latencies)
	if p95 > 100*time.Millisecond {
		t.Errorf("input latency p95 = %v, want ≤ 100ms", p95)
	}
	if worst > 500*time.Millisecond {
		t.Errorf("input latency max = %v, want ≤ 500ms", worst)
	}
}

package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
)

// syncBuffer is an io.Writer safe to read while the writer goroutine writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteControl([]byte(`{"jsonrpc":"2.0"}`)); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	if err := w.WriteData(3, []byte("hello")); err != nil {
		t.Fatalf("WriteData: %v", err)
	}
	w.Close()
	w.Wait()

	r := NewReader(&buf)
	first, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame control: %v", err)
	}
	if first.Kind != KindControl || first.Stream != 0 || string(first.Payload) != `{"jsonrpc":"2.0"}` {
		t.Fatalf("control frame = %+v, want the control payload on stream 0", first)
	}

	second, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame data: %v", err)
	}
	if second.Kind != KindData || second.Stream != 3 || string(second.Payload) != "hello" {
		t.Fatalf("data frame = %+v, want stream 3 carrying hello", second)
	}

	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame after the last frame = %v, want io.EOF", err)
	}
}

func TestReaderRejectsMalformedHeaders(t *testing.T) {
	tests := []struct {
		name   string
		kind   Kind
		stream uint32
		length uint32
	}{
		{name: "control frame on a data stream", kind: KindControl, stream: 1, length: 0},
		{name: "data frame on the control stream", kind: KindData, stream: 0, length: 0},
		{name: "unknown kind", kind: 0x7f, stream: 1, length: 0},
		{name: "length past the cap", kind: KindData, stream: 1, length: MaxFrameSize + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hdr [HeaderSize]byte
			hdr[0] = byte(tt.kind)
			binary.LittleEndian.PutUint32(hdr[1:5], tt.stream)
			binary.LittleEndian.PutUint32(hdr[5:9], tt.length)

			_, err := NewReader(bytes.NewReader(hdr[:])).ReadFrame()
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("ReadFrame(%s) = %v, want ErrProtocol", tt.name, err)
			}
		})
	}
}

func TestWriterRejectsDataOnTheControlStream(t *testing.T) {
	w := NewWriter(io.Discard)
	defer w.Close()

	if err := w.WriteData(0, []byte("x")); !errors.Is(err, ErrProtocol) {
		t.Fatalf("WriteData(0) = %v, want ErrProtocol", err)
	}
}

func TestWriterCoalescesConsecutiveDataFrames(t *testing.T) {
	out := &syncBuffer{}
	w := NewWriter(out)

	const chunks = 8
	for i := 0; i < chunks; i++ {
		if err := w.WriteData(1, []byte("0123456789")); err != nil {
			t.Fatalf("WriteData %d: %v", i, err)
		}
	}
	w.Close()
	w.Wait()

	r := NewReader(bytes.NewReader(out.Bytes()))
	frames, total := 0, 0
	for {
		f, err := r.ReadFrame()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if f.Stream != 1 {
			t.Fatalf("frame arrived on stream %d, want 1", f.Stream)
		}
		frames++
		total += len(f.Payload)
	}

	if total != chunks*10 {
		t.Fatalf("received %d bytes, want %d: coalescing lost data", total, chunks*10)
	}
	if frames >= chunks {
		t.Fatalf("received %d frames for %d writes: nothing was coalesced", frames, chunks)
	}
}

// gateWriter stalls its first write until released, so a test can fill the
// queues behind a write that is already in flight.
type gateWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	out     syncBuffer
}

func (g *gateWriter) Write(p []byte) (int, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	return g.out.Write(p)
}

// TestControlOvertakesTheDataBacklog pins the priority rule under a flood: a
// control frame queued while a data write is stalled and the data queue is
// full must go out before the backlog, not after it.
func TestControlOvertakesTheDataBacklog(t *testing.T) {
	g := &gateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	w := NewWriter(g)

	if err := w.WriteData(1, []byte("first")); err != nil {
		t.Fatalf("WriteData first: %v", err)
	}
	<-g.entered
	for i := 0; i < QueueDepth; i++ {
		if err := w.WriteData(1, bytes.Repeat([]byte("x"), 1024)); err != nil {
			t.Fatalf("WriteData %d: %v", i, err)
		}
	}
	if err := w.WriteControl([]byte("urgent")); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	close(g.release)
	w.Close()
	w.Wait()

	r := NewReader(bytes.NewReader(g.out.Bytes()))
	first, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if first.Kind != KindData || string(first.Payload) != "first" {
		t.Fatalf("first frame = kind %d %q, want the in-flight data frame", first.Kind, first.Payload)
	}
	second, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read second frame: %v", err)
	}
	if second.Kind != KindControl || string(second.Payload) != "urgent" {
		t.Fatalf("second frame = kind %d %q, want the control frame ahead of the backlog", second.Kind, second.Payload)
	}

	total := len(first.Payload)
	for {
		f, err := r.ReadFrame()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read backlog frame: %v", err)
		}
		if f.Kind != KindData {
			t.Fatalf("unexpected extra frame of kind %d", f.Kind)
		}
		total += len(f.Payload)
	}
	if want := len("first") + QueueDepth*1024; total != want {
		t.Fatalf("received %d data bytes, want %d: priority dropped data", total, want)
	}
}

func TestWriterRefusesWorkAfterClose(t *testing.T) {
	w := NewWriter(io.Discard)
	w.Close()
	w.Wait()

	if err := w.WriteControl([]byte("{}")); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("WriteControl after Close = %v, want ErrConnClosed", err)
	}
	if !errors.Is(w.Err(), ErrConnClosed) {
		t.Fatalf("Err() = %v, want ErrConnClosed", w.Err())
	}
}

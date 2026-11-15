package ipc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Kind separates the control plane from the terminal data plane on one wire.
type Kind byte

const (
	// KindControl carries one JSON-RPC message.
	KindControl Kind = 0x01
	// KindData carries raw terminal bytes for one stream.
	KindData Kind = 0x02
)

// MaxFrameSize caps an inbound payload. A larger length prefix is a protocol
// error and never an allocation: the length arrives from the peer.
const MaxFrameSize = 1 << 20

// headerSize is kind(1) + stream(4) + length(4).
const headerSize = 9

// controlStream is the only stream a control frame may use.
const controlStream uint32 = 0

// Frame is one unit on the wire. Stream is meaningful only for KindData.
type Frame struct {
	Kind    Kind
	Stream  uint32
	Payload []byte
}

// Reader decodes frames from one connection.
type Reader struct {
	r io.Reader
}

// NewReader returns a reader that decodes frames from r.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// ReadFrame returns the next frame, or io.EOF when the peer closed cleanly.
func (r *Reader) ReadFrame() (Frame, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, io.EOF
		}
		return Frame{}, fmt.Errorf("read frame header: %w", err)
	}

	f := Frame{
		Kind:   Kind(hdr[0]),
		Stream: binary.LittleEndian.Uint32(hdr[1:5]),
	}
	length := binary.LittleEndian.Uint32(hdr[5:9])

	switch {
	case f.Kind == KindControl && f.Stream != controlStream:
		return Frame{}, fmt.Errorf("control frame on stream %d: %w", f.Stream, ErrProtocol)
	case f.Kind == KindData && f.Stream == controlStream:
		return Frame{}, fmt.Errorf("data frame on the control stream: %w", ErrProtocol)
	case f.Kind != KindControl && f.Kind != KindData:
		return Frame{}, fmt.Errorf("unknown frame kind %#x: %w", byte(f.Kind), ErrProtocol)
	case length > MaxFrameSize:
		return Frame{}, fmt.Errorf("frame of %d bytes exceeds %d: %w", length, MaxFrameSize, ErrProtocol)
	}

	f.Payload = make([]byte, length)
	if _, err := io.ReadFull(r.r, f.Payload); err != nil {
		return Frame{}, fmt.Errorf("read frame payload: %w", err)
	}
	return f, nil
}

// QueueDepth is the depth of each writer queue. A full data queue blocks its
// producer, which is the backpressure a PTY reader needs under a log flood.
const QueueDepth = 64

// coalesceLimit bounds one write after merging, so a flood becomes bounded
// syscalls instead of one unbounded copy.
const coalesceLimit = 64 * 1024

// Writer serialises frames onto one connection. Control frames go first, and a
// run of data frames for one stream rides a single write.
type Writer struct {
	w        io.Writer
	control  chan Frame
	data     chan Frame
	closing  chan struct{}
	done     chan struct{}
	closingF atomic.Bool

	closeOnce sync.Once
	stopOnce  sync.Once
	mu        sync.Mutex
	err       error
}

// NewWriter returns a writer draining into w on its own goroutine.
func NewWriter(w io.Writer) *Writer {
	wr := &Writer{
		w:       w,
		control: make(chan Frame, QueueDepth),
		data:    make(chan Frame, QueueDepth),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go wr.loop()
	return wr
}

// WriteControl queues one control payload, copying it.
func (wr *Writer) WriteControl(payload []byte) error {
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("control payload of %d bytes exceeds %d: %w", len(payload), MaxFrameSize, ErrProtocol)
	}
	return wr.enqueue(wr.control, Frame{Kind: KindControl, Payload: append([]byte(nil), payload...)})
}

// WriteData queues terminal bytes for one stream, copying them.
func (wr *Writer) WriteData(stream uint32, p []byte) error {
	if stream == controlStream {
		return fmt.Errorf("data frame on the control stream: %w", ErrProtocol)
	}
	if len(p) == 0 {
		return nil
	}
	if len(p) > MaxFrameSize {
		p = p[:MaxFrameSize]
	}
	return wr.enqueue(wr.data, Frame{Kind: KindData, Stream: stream, Payload: append([]byte(nil), p...)})
}

// Close flushes the frames already queued and stops the writer. It does not
// wait: a write can block on a peer that stopped reading, so shutdown closes
// the transport first and lets that write fail. Call Wait to observe the end.
func (wr *Writer) Close() {
	wr.closeOnce.Do(func() {
		wr.closingF.Store(true)
		close(wr.closing)
	})
}

// Wait blocks until the writer has stopped.
func (wr *Writer) Wait() { <-wr.done }

// Done closes when the writer has stopped, so a reader can stop with it.
func (wr *Writer) Done() <-chan struct{} { return wr.done }

// Err reports why the writer stopped, or ErrConnClosed after a clean Close.
func (wr *Writer) Err() error {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	if wr.err != nil {
		return wr.err
	}
	return ErrConnClosed
}

func (wr *Writer) stop(err error) {
	wr.stopOnce.Do(func() {
		wr.mu.Lock()
		wr.err = err
		wr.mu.Unlock()
		close(wr.done)
	})
}

func (wr *Writer) enqueue(q chan<- Frame, f Frame) error {
	if wr.closingF.Load() {
		return fmt.Errorf("write after close: %w", ErrConnClosed)
	}

	select {
	case <-wr.done:
		return fmt.Errorf("write after stop: %w", wr.Err())
	default:
	}

	select {
	case <-wr.done:
		return fmt.Errorf("write after stop: %w", wr.Err())
	case q <- f:
		return nil
	}
}

func (wr *Writer) loop() {
	defer wr.stop(nil)

	for {
		select {
		case <-wr.done:
			return
		case <-wr.closing:
			wr.flush()
			return
		case f := <-wr.control:
			if !wr.write(f) {
				return
			}
			continue
		default:
		}

		select {
		case <-wr.done:
			return
		case <-wr.closing:
			wr.flush()
			return
		case f := <-wr.control:
			if !wr.write(f) {
				return
			}
		case f := <-wr.data:
			// Control frames queued before this one goes out first: a log flood
			// must not delay a cancel or a permission reply.
			if !wr.writePendingControl() {
				return
			}
			if !wr.writeAll(wr.drain(f)) {
				return
			}
		}
	}
}

// writePendingControl drains the control queue without waiting. It returns
// false only when a write failed.
func (wr *Writer) writePendingControl() bool {
	for {
		select {
		case f := <-wr.control:
			if !wr.write(f) {
				return false
			}
		default:
			return true
		}
	}
}

// flush writes what is already queued, control first. It terminates because
// enqueue refuses new frames once closing is set.
func (wr *Writer) flush() {
	for {
		if !wr.writePendingControl() {
			return
		}

		select {
		case f := <-wr.data:
			if !wr.writeAll(wr.drain(f)) {
				return
			}
		default:
			return
		}
	}
}

func (wr *Writer) writeAll(frames []Frame) bool {
	for _, f := range frames {
		if !wr.write(f) {
			return false
		}
	}
	return true
}

// drain merges the data frames already queued behind first, cutting the run at
// coalesceLimit. It never re-enqueues: this goroutine is the only consumer.
func (wr *Writer) drain(first Frame) []Frame {
	var out []Frame
	cur := first

	for len(cur.Payload) < coalesceLimit {
		select {
		case next := <-wr.data:
			if next.Stream != cur.Stream {
				out = append(out, cur)
				cur = next
				continue
			}
			room := coalesceLimit - len(cur.Payload)
			if len(next.Payload) > room {
				cur.Payload = append(cur.Payload, next.Payload[:room]...)
				out = append(out, cur)
				cur = Frame{Kind: KindData, Stream: next.Stream, Payload: next.Payload[room:]}
				continue
			}
			cur.Payload = append(cur.Payload, next.Payload...)
		default:
			return append(out, cur)
		}
	}
	return append(out, cur)
}

func (wr *Writer) write(f Frame) bool {
	buf := make([]byte, headerSize+len(f.Payload))
	buf[0] = byte(f.Kind)
	binary.LittleEndian.PutUint32(buf[1:5], f.Stream)
	binary.LittleEndian.PutUint32(buf[5:9], uint32(len(f.Payload)))
	copy(buf[headerSize:], f.Payload)

	if _, err := wr.w.Write(buf); err != nil {
		wr.stop(fmt.Errorf("write frame: %w", err))
		return false
	}
	return true
}

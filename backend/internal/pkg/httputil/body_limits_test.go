package httputil

import (
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// A zstd frame can declare a 512 MB window in a few bytes, and the decoder used
// to allocate that history before decoding anything. Such a frame must now be
// refused without the allocation.
func TestReadRequestBodyWithPrealloc_RejectsOversizedZstdWindowWithoutAllocatingIt(t *testing.T) {
	// Hand-built frame, because the encoder shrinks the window for a small input:
	// magic, a descriptor with no single-segment flag, window descriptor 0x98
	// (exponent 19 -> 2^(10+19) = 512 MB), then one last raw block.
	payload := []byte(`{"model":"x"}`)
	frame := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, 0x98}
	blockHeader := uint32(len(payload))<<3 | 1 // raw block, last
	frame = append(frame, byte(blockHeader), byte(blockHeader>>8), byte(blockHeader>>16))
	frame = append(frame, payload...)
	if zstd.MaxWindowSize != 1<<29 {
		t.Skip("fixture assumes the library's 512 MB default window cap")
	}

	req, err := http.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Encoding", "zstd")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = ReadRequestBodyWithPrealloc(req)
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatal("a frame declaring a window above the decoded-size cap must be rejected")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("rejecting the frame allocated %d MB; the declared window must not be allocated", allocated>>20)
	}
}

// A body that inflates past the cap is a 413, not a silently truncated document.
func TestReadRequestBodyWithPrealloc_ReportsOversizedDecompressedBody(t *testing.T) {
	var compressed bytes.Buffer
	gw, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		t.Fatalf("NewWriterLevel: %v", err)
	}
	if _, err := gw.Write(make([]byte, maxDecompressedBodySize+1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Encoding", "gzip")

	_, err = ReadRequestBodyWithPrealloc(req)
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("want *http.MaxBytesError, got %v", err)
	}
	if maxErr.Limit != maxDecompressedBodySize {
		t.Fatalf("limit = %d, want %d", maxErr.Limit, maxDecompressedBodySize)
	}
}

// Escaping grows each control byte sixfold. An oversized result is rejected up
// front, and an accepted one is allocated once at its exact size.
func TestNormalizeLenientJSONRequestBody_SizesEscapesBeforeAllocating(t *testing.T) {
	body := []byte(`{"a":"` + strings.Repeat("\x00", 1000) + `"}`)

	_, err := NormalizeLenientJSONRequestBody(body, int64(len(body)+100))
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("want *http.MaxBytesError for an escaped size over the limit, got %v", err)
	}

	out, err := NormalizeLenientJSONRequestBody(body, int64(len(body)+5000))
	if err != nil {
		t.Fatalf("exact-fit limit must pass: %v", err)
	}
	want := `{"a":"` + strings.Repeat(`\u0000`, 1000) + `"}`
	if string(out) != want {
		t.Fatalf("unexpected normalization result (len %d, want %d)", len(out), len(want))
	}
	if cap(out) != len(out) {
		t.Fatalf("output capacity %d, want exactly %d", cap(out), len(out))
	}

	// An escaped quote does not end the string, so the raw TAB after it is still
	// inside it and gets escaped; the count and the rewrite must agree on that.
	mixed := []byte("{\"a\":\"x\\\"\ty\"}")
	if got, err := NormalizeLenientJSONRequestBody(mixed, 1024); err != nil || string(got) != `{"a":"x\"\u0009y"}` {
		t.Fatalf("got %q, %v", got, err)
	}
}

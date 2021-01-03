package util

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sahib/brig/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestClamp(t *testing.T) {
	if Clamp(-1, 0, 1) != 0 {
		t.Errorf("Clamp: -1 is not in [0, 1]")
	}

	if Clamp(+1, 0, 1) != 1 {
		t.Errorf("Clamp: +1 should be [0, 1]")
	}

	if Clamp(0, 0, 1) != 0 {
		t.Errorf("Clamp: 0 should be [0, 1]")
	}

	if Clamp(+2, 0, 1) != 1 {
		t.Errorf("Clamp: 2 was not cut")
	}
}

func TestSizeAcc(t *testing.T) {
	N := 20
	data := []byte("Hello World, how are you today?")

	sizeAcc := &SizeAccumulator{}
	buffers := []*bytes.Buffer{}

	for i := 0; i < N; i++ {
		buf := bytes.NewBuffer(data)
		buffers = append(buffers, buf)
	}

	wg := &sync.WaitGroup{}
	wg.Add(N)

	for i := 0; i < N; i++ {
		go func(buf *bytes.Buffer) {
			for j := 0; j < len(data); j++ {
				miniBuf := []byte{0}
				buf.Read(miniBuf)
				if _, err := sizeAcc.Write(miniBuf); err != nil {
					t.Errorf("write(sizeAcc, miniBuf) failed: %v", err)
				}
			}

			wg.Done()
		}(buffers[i])
	}

	wg.Wait()
	if int(sizeAcc.Size()) != N*len(data) {
		t.Errorf("SizeAccumulator: Sizes got dropped, race condition?")
		t.Errorf(
			"Should be %v x %v = %v; was %v",
			len(data), N, len(data)*N, sizeAcc.Size(),
		)
	}
}

func TestTouch(t *testing.T) {
	// Test for fd leakage:
	N := 4097

	baseDir := filepath.Join(os.TempDir(), "touch-test")
	if err := os.Mkdir(baseDir, 0777); err != nil {
		t.Errorf("touch-test: Could not create temp dir: %v", err)
		return
	}

	defer func() {
		if err := os.RemoveAll(baseDir); err != nil {
			t.Errorf("touch-test: Could not remove temp-dir: %v", err)
		}
	}()

	for i := 0; i < N; i++ {
		touchPath := filepath.Join(baseDir, fmt.Sprintf("%d", i))
		if err := Touch(touchPath); err != nil {
			t.Errorf("touch-test: Touch() failed: %v", err)
			return
		}

		if _, err := os.Stat(touchPath); os.IsNotExist(err) {
			t.Errorf("touch-test: `%v` does not exist after Touch()", touchPath)
			return
		}
	}
}

type slowWriter struct{}

func (w slowWriter) Write(buf []byte) (int, error) {
	time.Sleep(500 * time.Millisecond)
	return 0, nil
}

func TestTimeoutWriter(t *testing.T) {
	fast := NewTimeoutWriter(&bytes.Buffer{}, 500*time.Millisecond)
	beforeFast := time.Now()
	fast.Write([]byte("Hello World"))
	fastTook := time.Since(beforeFast)

	if fastTook > 50*time.Millisecond {
		t.Errorf("TimeoutWriter did wait too long.")
		return
	}

	beforeSlow := time.Now()
	slow := NewTimeoutWriter(slowWriter{}, 250*time.Millisecond)
	slow.Write([]byte("Hello World"))
	slowTook := time.Since(beforeSlow)

	if slowTook > 300*time.Millisecond {
		t.Errorf("TimeoutWriter did not kill write fast enough.")
		return
	}

	if slowTook < 200*time.Millisecond {
		t.Errorf("TimeoutWriter did return too fast.")
		return
	}
}

func ExampleSizeAccumulator() {
	s := &SizeAccumulator{}
	teeR := io.TeeReader(bytes.NewReader([]byte("Hello, ")), s)
	io.Copy(os.Stdout, teeR)
	fmt.Printf("wrote %d bytes to stdout\n", s.Size())
	// Output: Hello, wrote 7 bytes to stdout
}

func TestLimitWriterSimple(t *testing.T) {
	tcs := []struct {
		limit     int64
		dummySize int64
		writes    int
		name      string
	}{
		{1024, 512, 3, "basic"},
		{1024, 512, 2, "exact"},
		{1022, 511, 2, "off-by-two"},
		{1023, 1024, 1, "off-mimus-one"},
		{1024, 1025, 1, "off-plus-one"},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			outBuf := &bytes.Buffer{}
			w := LimitWriter(outBuf, tc.limit)

			dummy := testutil.CreateDummyBuf(tc.dummySize)
			expected := make([]byte, 0)
			for i := 0; i < tc.writes; i++ {
				w.Write(dummy)
				expected = append(expected, dummy...)
			}

			expected = expected[:tc.limit]

			if outBuf.Len() != int(tc.limit) {
				t.Fatalf(
					"Length differs (got %d, want %d)",
					outBuf.Len(),
					tc.limit,
				)
			}

			if !bytes.Equal(expected, outBuf.Bytes()) {
				t.Fatalf("Data differs")
			}
		})
	}
}

func makePrefixReader(data []byte, rs io.ReadSeeker) *seekablePrefixReader {
	return &seekablePrefixReader{
		prefixReader: prefixReader{
			data: data,
			r:    rs,
		},
		s: rs,
	}
}

func TestPrefixReader(t *testing.T) {
	a := []byte{1, 2, 3}
	b := []byte{4, 5, 6}

	r := makePrefixReader(a, bytes.NewReader(b))
	data, err := ioutil.ReadAll(r)
	require.Nil(t, err)
	require.Equal(t, data, []byte{1, 2, 3, 4, 5, 6})
}

func TestPrefixReaderEmptyReader(t *testing.T) {
	a := []byte{1, 2, 3}
	b := []byte{}

	r := makePrefixReader(a, bytes.NewReader(b))
	data, err := ioutil.ReadAll(r)
	require.Nil(t, err)
	require.Equal(t, data, []byte{1, 2, 3})
}

func TestPrefixReaderEmptyBuffer(t *testing.T) {
	a := []byte{}
	b := []byte{4, 5, 6}

	r := makePrefixReader(a, bytes.NewReader(b))
	data, err := ioutil.ReadAll(r)
	require.Nil(t, err)
	require.Equal(t, data, []byte{4, 5, 6})
}

func TestPrefixReaderBothEmpty(t *testing.T) {
	a := []byte{}
	b := []byte{}

	r := makePrefixReader(a, bytes.NewReader(b))
	data, err := ioutil.ReadAll(r)
	require.Nil(t, err)
	require.Equal(t, data, []byte{})
}

func TestPrefixReaderPartial(t *testing.T) {
	a := []byte{1, 2, 3}
	b := []byte{4, 5, 6}

	r := makePrefixReader(a, bytes.NewReader(b))

	buf := make([]byte, 6)
	for i := 0; i < 6; i++ {
		n, err := r.Read(buf[i : i+1])
		require.Nil(t, err)
		require.Equal(t, n, 1)
		require.Equal(t, buf[:i+1], []byte{1, 2, 3, 4, 5, 6}[:i+1])
	}
}

func TestPrefixReaderSeekSize(t *testing.T) {
	a := []byte{1, 2, 3}
	b := []byte{1, 2, 3, 4, 5, 6}

	r := makePrefixReader(a, bytes.NewReader(b))
	size, err := r.Seek(0, io.SeekEnd)
	require.Nil(t, err)
	require.Equal(t, int64(6), int64(size))

	off, err := r.Seek(0, io.SeekStart)
	require.Nil(t, err)
	require.Equal(t, int64(0), off)

	curr, err := r.Seek(0, io.SeekCurrent)
	require.Nil(t, err)
	require.Equal(t, int64(0), curr)

	buf := &bytes.Buffer{}
	n, err := io.Copy(buf, r)
	require.Nil(t, err)
	require.Equal(t, int64(6), n)
	require.Equal(t, []byte{1, 2, 3, 4, 5, 6}, buf.Bytes())
}

func TestHeaderReader(t *testing.T) {
	tests := []struct {
		// name of the test
		name string

		// size of the buffer passed to Read()
		readBufSize int64

		// size of the dummy data (i.e. file size)
		testBufSize int64

		// max size of the header
		headBufSize int64
	}{
		{
			name:        "happy-path",
			readBufSize: 256,
			testBufSize: 2048,
			headBufSize: 1024,
		}, {
			name:        "large-read-buffer",
			readBufSize: 4096,
			testBufSize: 2048,
			headBufSize: 1024,
		}, {
			name:        "large-head-buffer",
			readBufSize: 512,
			testBufSize: 2048,
			headBufSize: 4096,
		}, {
			name:        "zero-head-buffer",
			readBufSize: 512,
			testBufSize: 2048,
			headBufSize: 0,
		}, {
			name:        "odd-read-buffer",
			readBufSize: 123,
			testBufSize: 2048,
			headBufSize: 1024,
		}, {
			name:        "odd-test-buffer",
			readBufSize: 256,
			testBufSize: 1234,
			headBufSize: 1024,
		}, {
			name:        "odd-head-buffer",
			readBufSize: 123,
			testBufSize: 2048,
			headBufSize: 1234,
		},
	}

	for _, test := range tests {
		for idx, suffix := range []string{"no-peek", "peek"} {
			t.Run(test.name+"/"+suffix, func(t *testing.T) {
				testHeaderReader(
					t,
					idx == 1,
					test.readBufSize,
					test.testBufSize,
					test.headBufSize,
				)
			})
		}
	}
}

func testHeaderReader(t *testing.T, usePeek bool, readBufSize, testBufSize, headBufSize int64) {
	dummy := testutil.CreateDummyBuf(testBufSize)
	hr := NewHeaderReader(bytes.NewReader(dummy), uint64(headBufSize))

	var peekedHdr []byte
	if usePeek {
		var err error
		peekedHdr, err = hr.Peek()
		require.NoError(t, err)
	}

	// Now read until io.EOF:
	bytesLeft := testBufSize
	dummyIter := dummy
	buf := make([]byte, readBufSize)
	for {
		n, err := hr.Read(buf)
		if err == io.EOF {
			break
		}

		require.NoError(t, err)

		expectedSize := readBufSize
		if testBufSize < readBufSize {
			expectedSize = testBufSize
		}

		// on odd buf numbers there might be a odd sized last read:
		if bytesLeft < expectedSize {
			expectedSize = bytesLeft
		}

		bytesLeft -= int64(n)

		// NOTE: io.Reader does not guarantee that n == expectedSize,
		// we might read less which is fine, then we should just repeat Read()-ing.
		require.GreaterOrEqual(t, int(expectedSize), n, "unexpected read buffer return")
		require.Equal(t, dummyIter[:n], buf[:n])
		dummyIter = dummyIter[n:]
	}

	// Check that the header is really the part at the start
	// and that it has the expected length.
	expectedSize := headBufSize
	if testBufSize < headBufSize {
		expectedSize = testBufSize
	}

	hdr := hr.Header()
	require.Len(t, hdr, int(expectedSize))
	require.Equal(t, hdr, dummy[:expectedSize])

	if usePeek {
		require.Equal(t, hdr, peekedHdr, "Peek() differs from Head()")
	}
}

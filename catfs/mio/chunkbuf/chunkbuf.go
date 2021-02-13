package chunkbuf

import (
	"io"

	"github.com/sahib/brig/util"
)

// ChunkBuffer represents a custom buffer struct with Read/Write and Seek support.
type ChunkBuffer struct {
	buf      []byte
	readOff  int64
	writeOff int64
	size     int64
}

const (
	maxChunkSize = 64 * 1024
)

func (c *ChunkBuffer) Write(p []byte) (int, error) {
	n := copy(c.buf[c.writeOff:c.size], p)
	c.writeOff += int64(n)
	c.size = util.Max64(c.size, c.writeOff)
	return n, nil
}

// Reset resets the buffer like bytes.Buffer
func (c *ChunkBuffer) Reset(data []byte) {
	c.readOff = 0
	c.writeOff = 0
	c.size = int64(len(data))
	c.buf = data
}

// Len tells you the current size of the buffer contents
func (c *ChunkBuffer) Len() int {
	return int(c.size - c.readOff)
}

func (c *ChunkBuffer) Read(p []byte) (int, error) {
	n := copy(p, c.buf[c.readOff:c.size])
	c.readOff += int64(n)
	if n < len(p) {
		return n, io.EOF
	}

	return n, nil
}

// Seek implements io.Seeker
func (c *ChunkBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		c.readOff += offset
	case io.SeekEnd:
		c.readOff = c.size + offset
	case io.SeekStart:
		c.readOff = offset
	}
	c.readOff = util.Min64(c.readOff, c.size)
	c.writeOff = c.readOff
	return c.readOff, nil
}

// Close is a no-op only existing to fulfill io.Closer
func (c *ChunkBuffer) Close() error {
	return nil
}

// WriteTo implements the io.WriterTo interface
func (c *ChunkBuffer) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(c.buf[c.readOff:])
	if err != nil {
		return 0, err
	}

	c.readOff += int64(n)
	return int64(n), nil
}

// NewChunkBuffer returns a ChunkBuffer with the given data. if data is nil a
// ChunkBuffer with 64k is returned.
// Note that chunkbuf will take over ownership over the buf.
func NewChunkBuffer(data []byte) *ChunkBuffer {
	if data == nil {
		data = make([]byte, maxChunkSize)
	}

	return &ChunkBuffer{buf: data, size: int64(len(data))}
}

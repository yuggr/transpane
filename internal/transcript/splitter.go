package transcript

import (
	"bytes"

	"github.com/yuggr/transpane/internal/config"
)

type rawLine struct {
	offset int64
	raw    []byte
}

// lineSplitter is a byte-offset aware line buffer that bounds memory for
// oversized lines. It never splits a UTF-8 sequence by itself; the reader
// validates complete lines.
type lineSplitter struct {
	pos      int64
	buf      []byte
	dropping bool
	maxLine  int
}

func newLineSplitter(start int64, maxLine int) *lineSplitter {
	return &lineSplitter{pos: start, maxLine: maxLine}
}

func (s *lineSplitter) feed(chunk []byte) ([]rawLine, int) {
	s.buf = append(s.buf, chunk...)
	lines := []rawLine{}
	oversized := 0
	for {
		if s.dropping {
			index := bytes.IndexByte(s.buf, '\n')
			if index < 0 {
				s.pos += int64(len(s.buf))
				s.buf = nil
				return lines, oversized
			}
			s.pos += int64(index + 1)
			s.buf = s.buf[index+1:]
			oversized++
			s.dropping = false
			continue
		}

		index := bytes.IndexByte(s.buf, '\n')
		if index < 0 {
			if len(s.buf) > s.maxLine {
				s.pos += int64(len(s.buf))
				s.buf = nil
				s.dropping = true
			}
			return lines, oversized
		}

		line := s.buf[:index+1]
		if len(line) > s.maxLine {
			s.pos += int64(len(line))
			s.buf = s.buf[index+1:]
			oversized++
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		lines = append(lines, rawLine{offset: s.pos, raw: raw})
		s.pos += int64(len(line))
		s.buf = s.buf[index+1:]
	}
}

func (s *lineSplitter) consumed() int64 { return s.pos + int64(len(s.buf)) }

func defaultMaxLine(max int) int {
	if max <= 0 {
		return config.MaxLineBytes
	}
	return max
}

package main

import (
	"bytes"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/term"
)

const maxSGRRawBytes = 64

// sgrMouseReader normalizes Arabic-Indic (U+0660..U+0669) and Persian
// (U+06F0..U+06F9) digits to ASCII '0'..'9' strictly inside SGR mouse escape
// sequences ("\x1b[<Pb;Px;PyM" / "m"). All other input, including Arabic text
// and Arabic-Indic/Persian digits outside SGR sequences, passes through unchanged.
type sgrMouseReader struct {
	src        io.Reader
	isTTY      bool
	fd         uintptr
	raw        []byte
	out        []byte
	pendingErr error
}

func newSGRMouseReader(src io.Reader) *sgrMouseReader {
	return &sgrMouseReader{src: src}
}

// sgrMouseFile wraps an *os.File (such as os.Stdin) with sgrMouseReader while
// preserving Fd, Name, Write, and Close so Bubble Tea raw mode and cancelreader
// continue to operate directly on the underlying terminal descriptor.
type sgrMouseFile struct {
	file   *os.File
	reader *sgrMouseReader
}

func newSGRMouseFile(f *os.File) *sgrMouseFile {
	if f == nil {
		return nil
	}
	fd := f.Fd()
	return &sgrMouseFile{
		file: f,
		reader: &sgrMouseReader{
			src:   f,
			isTTY: term.IsTerminal(int(fd)),
			fd:    fd,
		},
	}
}

func (f *sgrMouseFile) Read(p []byte) (int, error) {
	return f.reader.Read(p)
}

func (f *sgrMouseFile) Write(p []byte) (int, error) {
	return f.file.Write(p)
}

func (f *sgrMouseFile) Close() error {
	return f.file.Close()
}

func (f *sgrMouseFile) Fd() uintptr {
	return f.file.Fd()
}

func (f *sgrMouseFile) Name() string {
	return f.file.Name()
}

func (r *sgrMouseReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for len(r.out) == 0 {
		// On a live TTY, hold an incomplete escape/SGR prefix briefly only if
		// more bytes are already readable on the descriptor; otherwise flush
		// immediately so Esc / Alt+[ never stall waiting for another key.
		if len(r.raw) > 0 && r.isTTY {
			timeoutMs := 2
			if len(r.raw) >= 3 {
				timeoutMs = 20
			}
			if !waitFDReadable(r.fd, timeoutMs) {
				r.process(true)
				if len(r.out) > 0 {
					break
				}
			}
		}

		// Cap the raw read so normalized output fits within len(p) whenever
		// len(p) > len(r.raw). That avoids stranding buffered bytes behind
		// cancelreader's epoll_wait in Bubble Tea's 256-byte read loop.
		readCap := len(p)
		if len(r.raw) < len(p) {
			readCap = len(p) - len(r.raw)
		}
		tmp := make([]byte, readCap)
		n, err := r.src.Read(tmp)
		if n > 0 {
			r.raw = append(r.raw, tmp[:n]...)
		}
		r.process(err != nil)
		if len(r.out) > 0 {
			if err != nil {
				r.pendingErr = err
			}
			break
		}
		if err != nil {
			return 0, err
		}
	}

	n := copy(p, r.out)
	r.out = r.out[n:]
	if len(r.out) == 0 {
		r.out = nil
		if len(r.raw) == 0 && r.pendingErr != nil {
			err := r.pendingErr
			r.pendingErr = nil
			return n, err
		}
	}
	return n, nil
}

func (r *sgrMouseReader) process(eof bool) {
	for len(r.raw) > 0 {
		if r.raw[0] != 0x1b {
			idx := bytes.IndexByte(r.raw, 0x1b)
			if idx < 0 {
				r.out = append(r.out, r.raw...)
				r.raw = nil
				return
			}
			r.out = append(r.out, r.raw[:idx]...)
			r.raw = r.raw[idx:]
		}

		if len(r.raw) == 1 {
			if !eof {
				return
			}
			r.out = append(r.out, 0x1b)
			r.raw = nil
			return
		}
		if r.raw[1] != '[' {
			r.out = append(r.out, r.raw[0])
			r.raw = r.raw[1:]
			continue
		}
		if len(r.raw) == 2 {
			if !eof {
				return
			}
			r.out = append(r.out, r.raw[:2]...)
			r.raw = nil
			return
		}
		if r.raw[2] != '<' {
			r.out = append(r.out, r.raw[:2]...)
			r.raw = r.raw[2:]
			continue
		}

		consumed, needMore := r.tryNormalizeSGR(eof)
		if needMore {
			return
		}
		r.raw = r.raw[consumed:]
		if len(r.raw) == 0 {
			r.raw = nil
		}
	}
}

func (r *sgrMouseReader) tryNormalizeSGR(eof bool) (consumed int, needMore bool) {
	pos := 3 // after "\x1b[<"
	semicolons := 0
	curDigits := 0
	var norm [maxSGRRawBytes]byte
	normLen := 0

	for pos < len(r.raw) {
		if !utf8.FullRune(r.raw[pos:]) {
			if !eof && pos < maxSGRRawBytes {
				return 0, true
			}
			r.out = append(r.out, r.raw[:3]...)
			return 3, false
		}

		ch, size := utf8.DecodeRune(r.raw[pos:])
		if ch == utf8.RuneError && size == 1 {
			r.out = append(r.out, r.raw[:3]...)
			return 3, false
		}

		if d, ok := normalizeSGRDigit(ch); ok {
			if pos+size > maxSGRRawBytes || normLen >= len(norm) {
				r.out = append(r.out, r.raw[:3]...)
				return 3, false
			}
			curDigits++
			norm[normLen] = d
			normLen++
			pos += size
			continue
		}

		if ch == ';' && curDigits > 0 && semicolons < 2 {
			if pos+size > maxSGRRawBytes || normLen >= len(norm) {
				r.out = append(r.out, r.raw[:3]...)
				return 3, false
			}
			semicolons++
			curDigits = 0
			norm[normLen] = ';'
			normLen++
			pos += size
			continue
		}

		if (ch == 'M' || ch == 'm') && semicolons == 2 && curDigits > 0 {
			r.out = append(r.out, 0x1b, '[', '<')
			r.out = append(r.out, norm[:normLen]...)
			r.out = append(r.out, byte(ch))
			return pos + size, false
		}

		// Not a valid SGR mouse sequence; flush raw bytes untouched.
		if ch == 0x1b && pos > 3 {
			r.out = append(r.out, r.raw[:pos]...)
			return pos, false
		}
		if pos > 3 {
			r.out = append(r.out, r.raw[:pos]...)
			return pos, false
		}
		r.out = append(r.out, r.raw[:3]...)
		return 3, false
	}

	if !eof {
		return 0, true
	}
	r.out = append(r.out, r.raw...)
	return len(r.raw), false
}

func normalizeSGRDigit(r rune) (byte, bool) {
	switch {
	case r >= '0' && r <= '9':
		return byte(r), true
	case r >= '٠' && r <= '٩': // Arabic-Indic U+0660..U+0669
		return byte('0' + (r - '٠')), true
	case r >= '۰' && r <= '۹': // Extended Arabic-Indic / Persian U+06F0..U+06F9
		return byte('0' + (r - '۰')), true
	default:
		return 0, false
	}
}

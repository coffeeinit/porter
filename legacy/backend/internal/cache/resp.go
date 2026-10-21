package cache

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// This file implements the Redis RESP (v2) wire format — the subset the
// cache client needs (PING / AUTH / SELECT / GET / SET / DEL / FLUSHDB).
// RESP is stable enough to encode without pulling in a Redis client library.
//
// Requests look like:
//
//	*N\r\n  $len\r\n <arg> \r\n ...       (array of bulk strings)
//
// Replies are one of:
//
//	+simple-string\r\n
//	-error\r\n
//	:int\r\n
//	$len\r\n<bytes>\r\n   ($-1\r\n = nil)
//	*N\r\n<reply>...      (*-1\r\n = nil)

// respError is a Redis error reply ("-ERR ..."), surfaced as a Go error.
type respError string

func (e respError) Error() string { return string(e) }

// writeCommand encodes args as a RESP command array and flushes it to w.
func writeCommand(w *bufio.Writer, args ...[]byte) error {
	if _, err := w.WriteString("*" + strconv.Itoa(len(args)) + "\r\n"); err != nil {
		return err
	}
	for _, a := range args {
		hdr := "$" + strconv.Itoa(len(a)) + "\r\n"
		if _, err := w.WriteString(hdr); err != nil {
			return err
		}
		if _, err := w.Write(a); err != nil {
			return err
		}
		if _, err := w.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}

// readReply decodes the next RESP reply. Bare payloads decode as []byte
// (simple strings and bulk strings), integers as int, nil as (nil, nil),
// and arrays as []any. Error replies return a respError.
func readReply(r *bufio.Reader) (any, error) {
	line, err := readCRLFLine(r)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("cache: empty RESP reply line")
	}
	typ, body := line[0], line[1:]
	switch typ {
	case '+':
		return []byte(body), nil
	case '-':
		return nil, respError(body)
	case ':':
		n, err := strconv.Atoi(string(body))
		if err != nil {
			return nil, fmt.Errorf("resp: bad integer reply %q", body)
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(string(body))
		if err != nil {
			return nil, fmt.Errorf("resp: bad bulk length %q", body)
		}
		if n < 0 {
			return nil, nil // $-1 == nil bulk string
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf[:n], nil
	case '*':
		n, err := strconv.Atoi(string(body))
		if err != nil {
			return nil, fmt.Errorf("resp: bad array length %q", body)
		}
		if n < 0 {
			return nil, nil // *-1 == nil array
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := readReply(r)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("resp: unexpected reply type %q", typ)
	}
}

// readCRLFLine reads one line up to and including its \r\n terminator.
func readCRLFLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, fmt.Errorf("resp: malformed line %q", line)
	}
	return line[:len(line)-2], nil
}

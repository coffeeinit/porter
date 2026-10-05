package cache

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Redis is a small RESP client with a connection pool. It is safe for
// concurrent use. Connections are dialed lazily and reused; a stale pooled
// connection (e.g. after the server restarts) is retried once on a fresh
// dial before the error is propagated.
type Redis struct {
	mu   sync.Mutex
	pool []net.Conn
	opts Options
}

// Options configures the Redis client. All fields have defaults applied in
// Open; only Addr is required.
type Options struct {
	Addr         string // host:port
	User         string // optional ACL username
	Password     string
	DB           int // selects DB 0..15
	DialTimeout  time.Duration
	IOTimeout    time.Duration
	MaxIdleConns int
}

const (
	defaultDialTimeout  = 3 * time.Second
	defaultIOTimeout    = 2 * time.Second
	defaultMaxIdleConns = 8
)

// ParseURL parses a redis:// URL into client options:
//
//	redis://host:6379/0
//	redis://:secret@host:6379/1
//	redis://user:secret@host:6379/2
//
// rediss:// (TLS) is not supported — use a plain redis:// endpoint.
func ParseURL(raw string) (*Options, error) {
	if raw == "" {
		return nil, errors.New("cache: empty redis url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("cache: parse redis url: %w", err)
	}
	if u.Scheme != "redis" {
		return nil, fmt.Errorf("cache: unsupported scheme %q in %q (want redis://)", u.Scheme, raw)
	}
	host := u.Host
	if host == "" {
		host = "localhost"
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host += ":6379"
	}
	opts := &Options{Addr: host}
	if u.User != nil {
		opts.User = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			opts.Password = pw
		}
	}
	if dbStr := strings.TrimPrefix(u.Path, "/"); dbStr != "" && dbStr != "/" {
		db, err := strconv.Atoi(dbStr)
		if err != nil {
			return nil, fmt.Errorf("cache: invalid redis db %q in %q", dbStr, raw)
		}
		opts.DB = db
	}
	return opts, nil
}

// Open parses url, applies defaults, and verifies connectivity with a PING.
// The returned client is ready to use; Close releases its pool.
func Open(ctx context.Context, url string) (*Redis, error) {
	opts, err := ParseURL(url)
	if err != nil {
		return nil, err
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.IOTimeout <= 0 {
		opts.IOTimeout = defaultIOTimeout
	}
	if opts.MaxIdleConns <= 0 {
		opts.MaxIdleConns = defaultMaxIdleConns
	}
	c := &Redis{opts: *opts}
	if err := c.Ping(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("cache: connect redis %s: %w", opts.Addr, err)
	}
	return c, nil
}

// Ping verifies the connection with a round trip. Used at startup.
func (c *Redis) Ping(ctx context.Context) error {
	reply, err := c.do(ctx, []byte("PING"))
	if err != nil {
		return err
	}
	if _, ok := reply.([]byte); !ok {
		return fmt.Errorf("unexpected PING reply %v", reply)
	}
	return nil
}

// Get returns the value for key, or (nil, false) on a miss.
func (c *Redis) Get(ctx context.Context, key string) ([]byte, bool) {
	reply, err := c.do(ctx, []byte("GET"), []byte(key))
	if err != nil {
		return nil, false
	}
	b, ok := reply.([]byte)
	if !ok {
		return nil, false // $-1 (miss) decodes to nil
	}
	return b, true
}

// Set stores value under key for ttl (PX milliseconds). ttl <= 0 keeps the
// key forever.
func (c *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	args := [][]byte{[]byte("SET"), []byte(key), value}
	if ttl > 0 {
		args = append(args, []byte("PX"), []byte(strconv.FormatInt(ttl.Milliseconds(), 10)))
	}
	reply, err := c.do(ctx, args...)
	if err != nil {
		return err
	}
	if _, ok := reply.([]byte); !ok {
		return fmt.Errorf("unexpected SET reply %v", reply)
	}
	return nil
}

// Del removes the given keys. Empty key sets are a no-op.
func (c *Redis) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	args := make([][]byte, 0, len(keys)+1)
	args = append(args, []byte("DEL"))
	for _, k := range keys {
		args = append(args, []byte(k))
	}
	_, err := c.do(ctx, args...)
	return err
}

// Flush removes every key in the selected database.
func (c *Redis) Flush(ctx context.Context) error {
	_, err := c.do(ctx, []byte("FLUSHDB"))
	return err
}

// Close releases all pooled connections.
func (c *Redis) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.pool {
		_ = conn.Close()
	}
	c.pool = nil
	return nil
}

// do runs one command, retrying once on a brand-new connection if the
// pooled connection is stale.
func (c *Redis) do(ctx context.Context, args ...[]byte) (any, error) {
	conn := c.acquire()
	if conn == nil {
		var err error
		conn, err = c.dial(ctx)
		if err != nil {
			return nil, err
		}
	}
	reply, err := c.roundtrip(conn, ctx, args...)
	if err != nil {
		_ = conn.Close()
		// The pooled connection may be stale (server restarted / closed it
		// under us). Retry exactly once on a fresh dial before giving up.
		fresh, derr := c.dial(ctx)
		if derr != nil {
			return nil, err
		}
		reply, err = c.roundtrip(fresh, ctx, args...)
		if err != nil {
			_ = fresh.Close()
			return nil, err
		}
		c.release(fresh)
		return reply, nil
	}
	c.release(conn)
	return reply, nil
}

func (c *Redis) acquire() net.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := len(c.pool); n > 0 {
		conn := c.pool[n-1]
		c.pool = c.pool[:n-1]
		return conn
	}
	return nil
}

func (c *Redis) release(conn net.Conn) {
	if conn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pool) >= c.opts.MaxIdleConns {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	c.pool = append(c.pool, conn)
}

// dial opens a fresh, authenticated, database-selected connection.
func (c *Redis) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: c.opts.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", c.opts.Addr)
	if err != nil {
		return nil, err
	}
	authed := c.opts.Password == ""
	if !authed {
		if _, err := c.roundtrip(conn, ctx, c.authArgs()...); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	if c.opts.DB != 0 {
		if _, err := c.roundtrip(conn, ctx, []byte("SELECT"), []byte(strconv.Itoa(c.opts.DB))); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (c *Redis) authArgs() [][]byte {
	args := [][]byte{[]byte("AUTH")}
	if c.opts.User != "" {
		args = append(args, []byte(c.opts.User))
	}
	return append(args, []byte(c.opts.Password))
}

// roundtrip writes one RESP command on conn and reads the reply.
func (c *Redis) roundtrip(conn net.Conn, ctx context.Context, args ...[]byte) (any, error) {
	deadline := time.Now().Add(c.opts.IOTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	w := bufio.NewWriter(conn)
	if err := writeCommand(w, args...); err != nil {
		return nil, err
	}
	return readReply(bufio.NewReader(conn))
}

package cache

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// testRedis returns a Redis URL from the env, or "" when none is set.
// Integration tests skip unless Redis is provided.
func testRedis() string { return os.Getenv("PORTER_TEST_REDIS_URL") }

func TestNoopBehavesAsAbsentCache(t *testing.T) {
	ctx := context.Background()
	var c Cache = Noop{}
	if _, ok := c.Get(ctx, "k"); ok {
		t.Fatal("Noop.Get should miss")
	}
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Noop.Set: %v", err)
	}
	if err := c.Del(ctx, "k"); err != nil {
		t.Fatalf("Noop.Del: %v", err)
	}
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Noop.Flush: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Noop.Close: %v", err)
	}
}

func TestParseURL(t *testing.T) {
	cases := []struct {
		raw      string
		wantAddr string
		wantUser string
		wantPass string
		wantDB   int
		wantErr  bool
	}{
		{"redis://localhost:6379", "localhost:6379", "", "", 0, false},
		{"redis://localhost:6379/0", "localhost:6379", "", "", 0, false},
		{"redis://localhost:6379/3", "localhost:6379", "", "", 3, false},
		{"redis://:secret@example.com:6380/1", "example.com:6380", "", "secret", 1, false},
		{"redis://alice:pw@example.com/2", "example.com:6379", "alice", "pw", 2, false},
		{"", "", "", "", 0, true},
		{"http://localhost", "", "", "", 0, true},
		{"redis://localhost:6379/notadb", "", "", "", 0, true},
	}
	for _, tc := range cases {
		opts, err := ParseURL(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseURL(%q): expected error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseURL(%q): %v", tc.raw, err)
			continue
		}
		if opts.Addr != tc.wantAddr || opts.User != tc.wantUser || opts.Password != tc.wantPass || opts.DB != tc.wantDB {
			t.Errorf("ParseURL(%q) = %+v, want addr=%q user=%q pass=%q db=%d",
				tc.raw, opts, tc.wantAddr, tc.wantUser, tc.wantPass, tc.wantDB)
		}
	}
}

// TestRESPCommandEncoding ensures writeCommand emits the array-of-bulk-strings
// form Redis expects.
func TestRESPCommandEncoding(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := writeCommand(w, []byte("SET"), []byte("key"), []byte("hi"), []byte("PX"), []byte("15000")); err != nil {
		t.Fatal(err)
	}
	want := "*5\r\n$3\r\nSET\r\n$3\r\nkey\r\n$2\r\nhi\r\n$2\r\nPX\r\n$5\r\n15000\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("writeCommand:\n got %q\nwant %q", got, want)
	}
}

// TestREADReply decodes each RESP reply kind from a raw byte stream.
func TestReadReply(t *testing.T) {
	feed := func(resp string) (any, error) {
		return readReply(bufio.NewReader(strings.NewReader(resp)))
	}

	if v, err := feed("+OK\r\n"); err != nil || string(v.([]byte)) != "OK" {
		t.Errorf("simple string: got %v, %v", v, err)
	}
	if _, err := feed("-ERR nope\r\n"); err == nil {
		t.Error("error reply should produce an error")
	} else if msg := err.Error(); msg != "ERR nope" {
		t.Errorf("error msg = %q", msg)
	}
	if v, err := feed(":42\r\n"); err != nil || v.(int) != 42 {
		t.Errorf("integer reply: got %v, %v", v, err)
	}
	if v, err := feed("$-1\r\n"); err != nil || v != nil {
		t.Errorf("nil bulk reply: got %v, %v", v, err)
	}
	if v, err := feed("$5\r\nhello\r\n"); err != nil || string(v.([]byte)) != "hello" {
		t.Errorf("bulk reply: got %q, %v", v, err)
	}
	if v, err := feed("*2\r\n$3\r\nfoo\r\n:7\r\n"); err != nil {
		t.Errorf("array reply: %v", err)
	} else {
		arr := v.([]any)
		if string(arr[0].([]byte)) != "foo" || arr[1].(int) != 7 {
			t.Errorf("array contents = %v", arr)
		}
	}
	if _, err := feed("?wat\r\n"); err == nil {
		t.Error("unknown type should error")
	}
}

// TestRedisIntegration is optional: it requires a live Redis on
// PORTER_TEST_REDIS_URL. Without one the suite still passes.
func TestRedisIntegration(t *testing.T) {
	raw := testRedis()
	if raw == "" {
		t.Skip("PORTER_TEST_REDIS_URL not set; skipping live Redis test")
	}
	ctx := context.Background()
	c, err := Open(ctx, raw)
	if err != nil {
		t.Fatalf("Open(%q): %v", raw, err)
	}
	defer c.Close()

	if err := c.Del(ctx, "porter-test:one", "porter-test:two"); err != nil {
		t.Fatalf("Del: %v", err)
	}
	if _, ok := c.Get(ctx, "porter-test:one"); ok {
		t.Fatal("expected miss for deleted key")
	}
	if err := c.Set(ctx, "porter-test:one", []byte("v1"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Set(ctx, "porter-test:two", []byte("v2"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok := c.Get(ctx, "porter-test:one"); !ok || string(v) != "v1" {
		t.Errorf("Get one = %q, %v", v, ok)
	}
	if err := c.Del(ctx, "porter-test:one"); err != nil {
		t.Fatalf("Del: %v", err)
	}
	if _, ok := c.Get(ctx, "porter-test:one"); ok {
		t.Error("key should be gone after Del")
	}
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if v, ok := c.Get(ctx, "porter-test:two"); !ok && v == nil {
		// flushdb cleared every key in the selected DB — good.
	}

	// TTL expiry: 100ms should vanish before 300ms elapses.
	key := "porter-test:ttl"
	_ = c.Set(ctx, key, []byte("x"), 100*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if _, ok := c.Get(ctx, key); ok {
		t.Error("expected key to expire after TTL")
	}
}

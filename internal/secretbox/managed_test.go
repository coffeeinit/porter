package secretbox

import (
	"strings"
	"testing"
)

func TestParseExecRef(t *testing.T) {
	n, ok := ParseExecRef("exec:porter:DB_PASS")
	if !ok || n != "DB_PASS" {
		t.Fatalf("got %q,%v", n, ok)
	}
	for _, bad := range []string{"plain", "exec:porter:", "exec:porter:A B", "exec:other:X"} {
		if _, ok := ParseExecRef(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
}

func TestMergeManagedBlockIdempotent(t *testing.T) {
	base := "STATIC=1\n"
	vars := map[string]string{"B": "2", "A": "x y"}
	once := MergeManagedBlock(base, vars)
	twice := MergeManagedBlock(once, vars)
	if once != twice {
		t.Fatal("merge must be idempotent")
	}
	if !strings.Contains(once, "STATIC=1") || !strings.Contains(once, "A='x y'") {
		t.Fatal("base preserved + vars quoted:\n" + once)
	}
	// Rewrite with different vars replaces the block, keeps the base.
	thrice := MergeManagedBlock(once, map[string]string{"C": "3"})
	if strings.Contains(thrice, "A=") || !strings.Contains(thrice, "STATIC=1") {
		t.Fatal("block must be replaced wholesale:\n" + thrice)
	}
}

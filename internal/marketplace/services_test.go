package marketplace

import (
	"testing"
)

func TestFindTemplate(t *testing.T) {
	for _, name := range []string{"postgres", "redis", "mysql", "mariadb", "mongo", "minio"} {
		tmpl, err := FindTemplate(name)
		if err != nil {
			t.Fatal(err)
		}
		if tmpl.Image == "" || tmpl.VolumeMiB <= 0 || len(tmpl.Ports) == 0 {
			t.Fatalf("template %s incomplete", name)
		}
	}
	if _, err := FindTemplate("toaster"); err == nil {
		t.Fatal("unknown template must fail")
	}
}

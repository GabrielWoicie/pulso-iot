package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	const key = "PULSO_DOTENV_TEST_SECRET"
	for _, tc := range []struct {
		name, content      string
		existing           *string
		missing, wantError bool
		want               string
	}{
		{name: "loads file", content: key + "=test-only-value\n", want: "test-only-value"},
		{name: "quoted secret and CRLF", content: "# comment\r\nexport " + key + "='test # $ secret'\r\n", want: "test # $ secret"},
		{name: "environment wins", content: key + "=from-file\n", existing: ptrString("from-process"), want: "from-process"},
		{name: "empty environment is still explicit", content: key + "=from-file\n", existing: ptrString(""), want: ""},
		{name: "optional missing file", missing: true},
		{name: "invalid syntax is sanitized", content: "INVALID!KEY=DO_NOT_EXPOSE_TEST_VALUE\n", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, "")
			if tc.existing == nil {
				if e := os.Unsetenv(key); e != nil {
					t.Fatal(e)
				}
			} else {
				t.Setenv(key, *tc.existing)
			}
			path := filepath.Join(t.TempDir(), ".env")
			if !tc.missing {
				if e := os.WriteFile(path, []byte(tc.content), 0600); e != nil {
					t.Fatal(e)
				}
			}
			err := LoadDotEnv(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error status: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "DO_NOT_EXPOSE_TEST_VALUE") {
				t.Fatal("parser error exposed a secret")
			}
			if got := os.Getenv(key); got != tc.want {
				t.Fatal("unexpected environment value")
			}
		})
	}
	t.Run("unreadable file type", func(t *testing.T) {
		if err := LoadDotEnv(t.TempDir()); err == nil {
			t.Fatal("directory accepted as dotenv file")
		}
	})
}
func ptrString(v string) *string { return &v }

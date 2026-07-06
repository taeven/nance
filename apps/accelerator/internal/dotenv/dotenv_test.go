package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingOK(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_DoesNotOverrideEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("NANCE_TEST_DOTENV=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("NANCE_TEST_DOTENV", "from-env")
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("NANCE_TEST_DOTENV"); got != "from-env" {
		t.Fatalf("got %q want from-env", got)
	}
}

func TestLoad_SetsWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("NANCE_TEST_DOTENV_UNSET=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	_ = os.Unsetenv("NANCE_TEST_DOTENV_UNSET")
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("NANCE_TEST_DOTENV_UNSET"); got != "loaded" {
		t.Fatalf("got %q", got)
	}
}

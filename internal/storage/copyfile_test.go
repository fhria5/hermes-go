package storage

import "os"

func readFileAll(p string) ([]byte, error) { return os.ReadFile(p) }
func writeFileAll(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }

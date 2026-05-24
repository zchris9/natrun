// Package envfile loads simple KEY=VALUE pairs from a .env file into the
// process environment. Existing environment variables take precedence over
// the file, so production deployments can override .env without editing it.
package envfile

import (
	"bufio"
	"os"
	"strings"
)

// Load reads path and sets any KEY=VALUE pairs as environment variables,
// skipping keys already present in the environment. Missing file is not
// an error — callers using .env for local development can ignore it.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return scanner.Err()
}

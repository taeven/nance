// Package dotenv loads optional local env files for development.
package dotenv

import (
	"errors"
	"os"

	"github.com/joho/godotenv"
)

// Load reads .env then .env.local from the process working directory, if present.
// Missing files are ignored. Variables already set in the environment are never
// overwritten (so shell/export/Docker env always win).
func Load() error {
	for _, name := range []string{".env", ".env.local"} {
		if err := loadFile(name); err != nil {
			return err
		}
	}
	return nil
}

func loadFile(name string) error {
	if _, err := os.Stat(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	// godotenv.Load does not override existing env vars.
	return godotenv.Load(name)
}

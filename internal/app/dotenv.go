package app

import (
	"errors"
	"os"

	"github.com/joho/godotenv"
)

// LoadDotEnv loads an optional file without overriding the process environment.
// Only the caller's explicit path is read; no parent directories are searched.
// Parser errors can contain secret values, so never return their raw contents.
func LoadDotEnv(path string) error {
	if err := godotenv.Load(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("não foi possível carregar .env; verifique a sintaxe e as permissões (conteúdo omitido)")
	}
	return nil
}

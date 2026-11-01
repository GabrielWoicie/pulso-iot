package main

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestRunLoadsDotEnvBeforeConfig(t *testing.T) {
	for _, cmd := range []string{"server", "bootstrap-admin", "check-broker"} {
		t.Run(cmd, func(t *testing.T) {
			for _, key := range []string{"DATABASE_URL", "JWT_SECRET", "MQTT_CREDENTIAL_KEY_B64"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("MQTT_BROKER", "mqtts://localhost:8883")
			t.Setenv("EMQX_API_URL", "http://127.0.0.1:18083")
			t.Chdir(t.TempDir())
			content := "DATABASE_URL=postgres://%zz\nJWT_SECRET=" + strings.Repeat("test-only-", 4) + "\nMQTT_CREDENTIAL_KEY_B64=" + base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"
			if err := os.WriteFile(".env", []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			previous := os.Args
			t.Cleanup(func() { os.Args = previous })
			os.Args = []string{"pulso-iot"}
			if cmd != "server" {
				os.Args = append(os.Args, cmd)
			}
			// Invalid DSN fails before any socket is opened, after dotenv/config validation.
			if err := run(); err == nil || err.Error() != "DATABASE_URL inválida" {
				t.Fatalf("configuration was not loaded before database validation: %v", err)
			}
		})
	}
}

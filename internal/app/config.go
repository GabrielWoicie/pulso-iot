package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const Prefix = "desbravatech/v1/devices/"

type Config struct {
	DatabaseURL, Listen, JWTSecret, MQTTBroker, MQTTUser, MQTTPassword, MQTTClientID string
	EMQXURL, EMQXKey, EMQXSecret, Origin                                             string
	Key                                                                              []byte
	TokenTTL, CommandTimeout                                                         time.Duration
	MaxDevices, MaxMessages                                                          int
}

func LoadConfig() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Listen: env("HTTP_ADDR", "127.0.0.1:8080"), JWTSecret: os.Getenv("JWT_SECRET"), MQTTBroker: env("MQTT_BROKER", "mqtts://broker.woicie.com.br:8883"), MQTTUser: env("MQTT_USERNAME", "svc-backend"), MQTTClientID: env("MQTT_CLIENT_ID", "svc-backend"), MQTTPassword: os.Getenv("MQTT_PASSWORD"), EMQXURL: env("EMQX_API_URL", "http://127.0.0.1:18083"), EMQXKey: os.Getenv("EMQX_API_KEY"), EMQXSecret: os.Getenv("EMQX_API_SECRET"), Origin: os.Getenv("CORS_ORIGIN"), TokenTTL: 15 * time.Minute, CommandTimeout: 30 * time.Second, MaxDevices: 10, MaxMessages: 1000}
	var e error
	c.Key, e = base64.StdEncoding.DecodeString(os.Getenv("MQTT_CREDENTIAL_KEY_B64"))
	if e != nil || len(c.Key) != 32 {
		return c, errors.New("MQTT_CREDENTIAL_KEY_B64 deve conter 32 bytes em base64")
	}
	if c.DatabaseURL == "" || len(c.JWTSecret) < 32 {
		return c, errors.New("configure DATABASE_URL e JWT_SECRET com ao menos 32 caracteres")
	}
	u, e := url.Parse(c.MQTTBroker)
	if e != nil || u.Scheme != "mqtts" || u.Hostname() == "" || u.Port() != "8883" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("MQTT_BROKER deve ser mqtts://hostname:8883")
	}
	u, e = url.Parse(c.EMQXURL)
	if e != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname()))) {
		return c, errors.New("EMQX_API_URL exige HTTPS ou HTTP em loopback")
	}

	return c, nil
}
func loopback(s string) bool {
	ip := net.ParseIP(s)
	return s == "localhost" || ip != nil && ip.IsLoopback()
}
func env(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}
func RandomSecret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Seal(key []byte, id, password string) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, e
	}
	return g.Seal(n, n, []byte(password), []byte("pulso_iot:mqtt_credentials:v1:"+id)), nil
}
func Open(key []byte, id string, data []byte) (string, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(data) < g.NonceSize()+g.Overhead()+1 {
		return "", errors.New("credencial indisponível")
	}
	p, e := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte("pulso_iot:mqtt_credentials:v1:"+id))
	if e != nil {
		return "", errors.New("credencial não pôde ser decifrada")
	}
	return string(p), nil
}
func validPassword(s string) bool {
	if len(s) < 12 || len(s) > 72 {
		return false
	}
	var a, b, c bool
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			a = true
		} else if r >= '0' && r <= '9' {
			b = true
		} else {
			c = true
		}
	}
	return a && b && c
}
func validMQTTPassword(s string) bool {
	return len(s) >= 12 && len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n")
}
func boundedInt(s string, d, min, max int) (int, error) {
	if s == "" {
		return d, nil
	}
	n, e := strconv.Atoi(s)
	if e != nil || n < min || n > max {
		return 0, fmt.Errorf("inteiro fora de %d..%d", min, max)
	}
	return n, nil
}

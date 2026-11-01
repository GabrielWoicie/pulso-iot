// Package config loads and validates the simulator's public configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return errors.New("duração deve ser texto, como 5s")
	}
	v, e := time.ParseDuration(n.Value)
	*d = Duration(v)
	if e != nil {
		return errors.New("duração inválida; use 5s, 500ms, 1m")
	}
	return nil
}
func (d Duration) Value() time.Duration { return time.Duration(d) }

type Range struct {
	Min float64 `yaml:"min"`
	Max float64 `yaml:"max"`
}
type Sensors struct {
	Temperature Range `yaml:"temperature_c"`
	Humidity    Range `yaml:"humidity_pct"`
}
type Device struct {
	ID        string   `yaml:"id"`
	ClientID  string   `yaml:"client_id"`
	Username  string   `yaml:"username"`
	Password  string   `yaml:"password" json:"-"`
	QoS       *int     `yaml:"qos"`
	Interval  Duration `yaml:"interval"`
	Telemetry Sensors  `yaml:"telemetry"`
}
type Config struct {
	Broker           string   `yaml:"broker"`
	MQTTVersion      string   `yaml:"mqtt_version"`
	QoS              int      `yaml:"qos"`
	ResponseQoS      int      `yaml:"response_qos"`
	Interval         Duration `yaml:"interval"`
	ConnectTimeout   Duration `yaml:"connect_timeout"`
	OperationTimeout Duration `yaml:"operation_timeout"`
	ShutdownTimeout  Duration `yaml:"shutdown_timeout"`
	ReconnectMax     Duration `yaml:"reconnect_max"`
	GracePeriod      Duration `yaml:"grace_period"`
	Seed             *int64   `yaml:"seed"`
	Devices          []Device `yaml:"devices"`
}

var validID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func ValidID(id string) bool { return validID.MatchString(id) }
func Defaults() Config {
	return Config{MQTTVersion: "5.0", QoS: 1, ResponseQoS: 1, Interval: Duration(5 * time.Second), ConnectTimeout: Duration(20 * time.Second), OperationTimeout: Duration(10 * time.Second), ShutdownTimeout: Duration(5 * time.Second), ReconnectMax: Duration(30 * time.Second), GracePeriod: Duration(2 * time.Second)}
}
func Load(path string) (Config, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Config{}, errors.New("não foi possível ler o arquivo de configuração")
	}
	return Parse(b, os.LookupEnv)
}

// Parse expands scalar values after YAML parsing: secret punctuation cannot alter YAML structure.
func Parse(data []byte, lookup func(string) (string, bool)) (Config, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if e := dec.Decode(&doc); e != nil {
		return Config{}, errors.New("YAML inválido (conteúdo omitido para proteger credenciais)")
	}
	var extra yaml.Node
	if e := dec.Decode(&extra); e != io.EOF {
		return Config{}, errors.New("configure apenas um documento YAML")
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return Config{}, errors.New("raiz YAML deve ser um mapa")
	}
	var expand func(*yaml.Node) error
	expand = func(n *yaml.Node) error {
		if n.Kind == yaml.AliasNode {
			return errors.New("aliases YAML não são permitidos")
		}
		if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
			var missing string
			n.Value = envRef.ReplaceAllStringFunc(n.Value, func(s string) string {
				key := envRef.FindStringSubmatch(s)[1]
				v, ok := lookup(key)
				if !ok || v == "" {
					missing = key
				}
				return v
			})
			if missing != "" {
				return fmt.Errorf("variável de ambiente obrigatória ausente ou vazia: %s", missing)
			}
		}
		for _, c := range n.Content {
			if e := expand(c); e != nil {
				return e
			}
		}
		return nil
	}
	if e := expand(&doc); e != nil {
		return Config{}, e
	}
	// Re-encode expanded nodes and use KnownFields to reject misspellings.
	b, e := yaml.Marshal(&doc)
	if e != nil {
		return Config{}, errors.New("falha ao interpretar YAML")
	}
	c := Defaults()
	strict := yaml.NewDecoder(bytes.NewReader(b))
	strict.KnownFields(true)
	if e = strict.Decode(&c); e != nil {
		return Config{}, errors.New("campos YAML desconhecidos, duplicados ou com tipo inválido (valores omitidos)")
	}
	if e = c.Normalize(); e != nil {
		return Config{}, e
	}
	return c, nil
}
func (c *Config) Normalize() error {
	u, e := url.Parse(c.Broker)
	if e != nil || u == nil || u.Scheme != "mqtts" || u.Hostname() == "" || u.Port() != "8883" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("broker deve usar mqtts://hostname:8883, sem credenciais, caminho ou parâmetros")
	}
	if c.MQTTVersion != "5.0" {
		return errors.New("mqtt_version deve ser 5.0")
	}
	if c.QoS < 0 || c.QoS > 2 || c.ResponseQoS < 0 || c.ResponseQoS > 2 {
		return errors.New("qos e response_qos devem estar entre 0 e 2")
	}
	for _, v := range []struct {
		name string
		d    Duration
	}{{"interval", c.Interval}, {"connect_timeout", c.ConnectTimeout}, {"operation_timeout", c.OperationTimeout}, {"shutdown_timeout", c.ShutdownTimeout}, {"reconnect_max", c.ReconnectMax}} {
		if v.d <= 0 {
			return fmt.Errorf("%s deve ser positivo", v.name)
		}
	}
	if c.ReconnectMax < Duration(time.Second) {
		return errors.New("reconnect_max deve ser >= 1s")
	}
	if c.GracePeriod < 0 {
		return errors.New("grace_period não pode ser negativo")
	}
	if len(c.Devices) == 0 {
		return errors.New("configure ao menos um dispositivo")
	}
	seen := map[string]bool{}
	for i := range c.Devices {
		d := &c.Devices[i]
		if !ValidID(d.ID) {
			return fmt.Errorf("devices[%d]: id inválido", i)
		}
		if seen[d.ID] {
			return fmt.Errorf("devices[%d]: id duplicado", i)
		}
		seen[d.ID] = true
		if d.ClientID == "" {
			d.ClientID = d.ID
		}
		if d.ClientID != d.ID {
			return fmt.Errorf("devices[%d]: client_id deve ser igual a id (identidade provisionada e ACL)", i)
		}
		if d.Username == "" {
			d.Username = d.ClientID
		}
		if strings.TrimSpace(d.Password) == "" {
			return fmt.Errorf("devices[%d]: senha obrigatória", i)
		}
		q := c.QoS
		if d.QoS != nil {
			q = *d.QoS
		}
		if q < 0 || q > 2 {
			return fmt.Errorf("devices[%d]: qos inválido", i)
		}
		d.QoS = new(int)
		*d.QoS = q
		if d.Interval == 0 {
			d.Interval = c.Interval
		}
		if d.Interval < 0 {
			return fmt.Errorf("devices[%d]: intervalo deve ser positivo", i)
		}
		for _, r := range []Range{d.Telemetry.Temperature, d.Telemetry.Humidity} {
			if math.IsNaN(r.Min) || math.IsNaN(r.Max) || math.IsInf(r.Min, 0) || math.IsInf(r.Max, 0) || r.Min > r.Max {
				return fmt.Errorf("devices[%d]: limites inválidos", i)
			}
		}
		if d.Telemetry.Temperature.Min < -100 || d.Telemetry.Temperature.Max > 150 {
			return fmt.Errorf("devices[%d]: temperatura deve estar entre -100 e 150 Celsius", i)
		}
		if d.Telemetry.Humidity.Min < 0 || d.Telemetry.Humidity.Max > 100 {
			return fmt.Errorf("devices[%d]: umidade deve estar entre 0 e 100", i)
		}
	}
	return nil
}

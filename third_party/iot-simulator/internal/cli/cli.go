package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/woicie/iot-simulator/pkg/config"
	"github.com/woicie/iot-simulator/pkg/runner"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const Version = "1.0.0"

type Options struct {
	Config, Output string
	Request        runner.RunRequest
	Finite         bool
	Timeout        time.Duration
	Help, Version  bool
}

func Parse(args []string, out io.Writer) (Options, error) {
	o := Options{Config: "config.yaml"}
	if len(args) > 0 && args[0] == "run" {
		o.Finite = true
		args = args[1:]
	}
	f := flag.NewFlagSet("tcc-simulator", flag.ContinueOnError)
	f.SetOutput(out)
	var ids string
	var q int
	var interval, grace time.Duration
	f.StringVar(&o.Config, "config", "config.yaml", "arquivo YAML")
	f.StringVar(&ids, "devices", "", "IDs separados por vírgula; omitido = todos")
	f.IntVar(&o.Request.Messages, "messages", 0, "tentativas de telemetria por dispositivo (somente run; padrão 20)")
	f.DurationVar(&interval, "interval", 0, "sobrescreve intervalo por dispositivo, ex.: 1s")
	f.IntVar(&q, "qos", -1, "sobrescreve QoS de telemetria: 0, 1 ou 2")
	f.DurationVar(&grace, "grace-period", 0, "tempo adicional para comandos após telemetria (padrão YAML: 2s)")
	f.DurationVar(&o.Timeout, "timeout", 0, "limite total da execução; 0 = sem limite")
	f.StringVar(&o.Output, "output-json", "", "caminho do relatório JSON final")
	f.BoolVar(&o.Help, "help", false, "exibe ajuda")
	f.BoolVar(&o.Version, "version", false, "exibe versão")
	f.Usage = func() {
		fmt.Fprintln(out, "Uso: tcc-simulator [run] --config config.yaml [opções]\nSem run: modo contínuo. Com run: execução finita.\nVariáveis ${VAR} são resolvidas pelo ambiente; .env não é carregado automaticamente.")
		f.PrintDefaults()
	}
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			o.Help = true
			return o, nil
		}
		return o, e
	}
	if o.Help {
		f.Usage()
		return o, nil
	}
	if o.Version {
		return o, nil
	}
	if f.NArg() != 0 {
		return o, errors.New("argumento inesperado; use --help")
	}
	seen := map[string]bool{}
	f.Visit(func(v *flag.Flag) { seen[v.Name] = true })
	if seen["qos"] {
		if q < 0 || q > 2 {
			return o, errors.New("--qos deve ser 0, 1 ou 2")
		}
		o.Request.QoS = &q
	}
	if seen["interval"] {
		if interval <= 0 {
			return o, errors.New("--interval deve ser positivo")
		}
		o.Request.Interval = interval
	}
	if seen["grace-period"] {
		if grace < 0 {
			return o, errors.New("--grace-period não pode ser negativo")
		}
		o.Request.GracePeriod = &grace
	}
	if o.Timeout < 0 {
		return o, errors.New("--timeout não pode ser negativo")
	}
	if ids != "" {
		for _, id := range strings.Split(ids, ",") {
			id = strings.TrimSpace(id)
			if !config.ValidID(id) {
				return o, errors.New("--devices contém ID inválido")
			}
			o.Request.DeviceIDs = append(o.Request.DeviceIDs, id)
		}
	} else if seen["devices"] {
		return o, errors.New("--devices não pode ser vazio")
	}
	if o.Finite {
		if !seen["messages"] {
			o.Request.Messages = 20
		}
		if o.Request.Messages <= 0 {
			return o, errors.New("run exige --messages positivo")
		}
	} else if seen["messages"] {
		return o, errors.New("--messages exige subcomando run")
	}
	if o.Output != "" {
		a, _ := filepath.Abs(o.Config)
		b, _ := filepath.Abs(o.Output)
		if strings.EqualFold(a, b) {
			return o, errors.New("relatório não pode substituir a configuração")
		}
	}
	return o, nil
}

// Execute returns a process exit code; all core behavior stays in pkg/runner.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, e := Parse(args, stdout)
	if e != nil {
		fmt.Fprintln(stderr, "Erro:", e)
		return 2
	}
	if o.Help {
		return 0
	}
	if o.Version {
		fmt.Fprintln(stdout, "tcc-simulator", Version)
		return 0
	}
	c, e := config.Load(o.Config)
	if e != nil {
		fmt.Fprintln(stderr, "Configuração:", e)
		return 2
	}
	r, e := runner.New(c, nil)
	if e != nil {
		fmt.Fprintln(stderr, "Configuração:", e)
		return 2
	}
	if _, e = runner.Select(c, o.Request); e != nil {
		fmt.Fprintln(stderr, "Solicitação:", e)
		return 2
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	result, runErr := r.Run(ctx, o.Request, func(ev runner.Event) {
		id := ev.DeviceID
		if id == "" {
			id = "runner"
		}
		fmt.Fprintf(stderr, "%s [%s] %s\n", ev.Timestamp.Format(time.RFC3339), id, ev.Message)
	})
	fmt.Fprintf(stdout, "Execução %s: %s\n", result.RunID, result.Status)
	for _, d := range result.Devices {
		fmt.Fprintf(stdout, "[%s] tentativas=%d concluídas=%d broker_ack=%d falhas=%d comandos=%d respostas=%d\n", d.DeviceID, d.Attempted, d.Published, d.BrokerAcknowledged, d.Failed, d.CommandsReceived, d.ResponsesPublished)
	}
	fmt.Fprintln(stdout, "Publicação/ACK MQTT não comprova entrega ao consumidor. Esta execução é funcional e independente do benchmark.")
	if o.Output != "" {
		if e = WriteResult(o.Output, result); e != nil {
			fmt.Fprintln(stderr, "Erro ao salvar relatório:", e)
			return 1
		}
		fmt.Fprintln(stdout, "Relatório:", o.Output)
	}
	if errors.Is(runErr, context.Canceled) {
		if o.Finite {
			return 130
		}
		return 0
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		return 124
	}
	if runErr != nil {
		return 1
	}
	return 0
}
func WriteResult(path string, r runner.RunResult) error {
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return errors.New("não foi possível serializar relatório")
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".iot-result-*")
	if e != nil {
		return errors.New("diretório de saída indisponível")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(append(b, '\n')); e != nil {
		f.Close()
		return errors.New("falha ao gravar relatório")
	}
	if e = f.Close(); e != nil {
		return errors.New("falha ao fechar relatório")
	}
	if e = os.Rename(tmp, path); e != nil {
		return errors.New("não foi possível substituir o relatório de destino")
	}
	return nil
}

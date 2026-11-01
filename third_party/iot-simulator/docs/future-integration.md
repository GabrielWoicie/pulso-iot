# Reutilização pelo futuro backend Go

Esta versão entrega a biblioteca de execução e a CLI. Não implementa servidor HTTP, rota, persistência, React, autenticação de usuários ou provisionamento do EMQX.

## Importação por outro módulo

O código reutilizável está em `pkg/config` e `pkg/runner`, pois pacotes Go `internal` não poderiam ser importados diretamente por um backend em repositório distinto.

Enquanto não houver um repositório publicado, use `replace` no go.mod do backend (exemplo de caminho relativo):

```go
require github.com/woicie/iot-simulator v0.0.0
replace github.com/woicie/iot-simulator => ../iot-simulator
```

Depois de publicar o código no repositório correto, o module path pode ser atualizado e versionado. Nenhum repositório foi criado nesta entrega.

## Chamada da lógica compartilhada

Exemplo de função que pode viver no backend futuro, usando exatamente o mesmo runner da CLI:

```go
package tests

import (
    "context"
    "time"

    "github.com/woicie/iot-simulator/pkg/config"
    "github.com/woicie/iot-simulator/pkg/runner"
)

func Execute(ctx context.Context, c config.Config, progress runner.Sink) (runner.RunResult, error) {
    engine, err := runner.New(c, nil) // nil = cliente MQTT/TLS real
    if err != nil {
        return runner.RunResult{}, err
    }
    qos := 1
    grace := 2 * time.Second
    return engine.Run(ctx, runner.RunRequest{
        DeviceIDs: []string{"dev-001", "dev-002"},
        Messages: 20,
        Interval: time.Second,
        QoS: &qos,
        GracePeriod: &grace,
    }, progress)
}
```

`Run` é síncrono e bloqueia até todos os clientes serem fechados. Para execução em segundo plano, o backend poderá chamá-lo em uma goroutine gerenciada por serviço, com contexto próprio e timeout. Não ligue uma tarefa longa diretamente ao contexto de um request HTTP que já terá respondido com aceite; armazene o cancelamento de forma controlada. Para interromper, invoque o cancel associado à execução. O retorno preserva resultado parcial e erro de contexto.

## Modelo HTTP ilustrativo

Uma futura rota POST /api/v1/tests poderia receber:

```json
{"device_ids":["dev-001","dev-002"],"messages":20,"interval_ms":1000,"qos":1}
```

Traduza `interval_ms` para `time.Duration(intervalMS) * time.Millisecond` após validar um limite superior para impedir overflow. `RunRequest.Interval` é time.Duration e, se serializado diretamente por encoding/json, usa **nanosegundos**. Portanto, não desserialize o JSON HTTP ilustrativo diretamente em RunRequest; mantenha um DTO da API com unidades explícitas. Para o modo de testes HTTP, exija messages > 0, intervalo >= 1ms e limites de dispositivos/tentativas apropriados.

`QoS` é ponteiro para distinguir zero de omitido. `GracePeriod` também é ponteiro; nil herda o YAML, ponteiro para zero desativa a janela extra. Interval zero herda valores por dispositivo; `Messages=0` representa contínuo na API interna, mas deve ser recusado pela futura rota de testes finitos.

`runner.Select` permite validar seleção e substituições antes de conectar. A configuração já deve conter dispositivos provisionados e senhas resolvidas pelo mecanismo seguro do backend, sem expô-las ao navegador. Em produção futura, o backend pode montar `config.Config` em memória a partir de cadastro e cofre de segredos; não é obrigatório depender de YAML.

## Progresso, consulta e resultado

O Sink recebe eventos estruturados com run_id, device_id, timestamp, kind, message e, para telemetria, contadores parciais de attempted/published. Exemplos de kind: run_started, connected, subscribed, disconnected, connect_error, telemetry, command, command_response, error e run_finished. O Sink é serializado pelo runner e deve ser rápido. Encaminhe eventos para um canal com buffer com política explícita de overflow ou para uma fila do backend; não faça gravação lenta ou acesso remoto diretamente no callback.

O primeiro evento fornece o run_id. Eventos não são uma cópia completa de DeviceResult e não substituem o resultado final. No retorno, guarde o RunResult completo, inclusive quando err != nil. O backend poderá indexá-lo por run_id e oferecer consulta futura por ID. O runner não mantém um registro global de execuções, não implementa polling HTTP nem um banco interno. A CLI consulta o resultado pelo retorno e o grava em JSON.

Mantenha a ordem: registrar tarefa aceita → iniciar worker → acompanhar eventos → receber resultado final → armazenar resultado → marcar estado terminal. A implementação de aceite, autenticação, consulta e notificação pertence ao backend futuro.

## Concorrência e persistência futuras

Não execute simultaneamente duas tarefas com os mesmos device_ids, nem concorra com a CLI/MQTTX usando o mesmo Client ID. O broker pode substituir uma conexão, produzindo reconexões recíprocas. O backend deverá reservar dispositivos por tarefa e estabelecer limites de concorrência.

Sugestão de persistência futura: tabela de execuções com run_id, parâmetros efetivos, timestamps, status, motivo e JSON final; tabela de resultados por dispositivo se consultas relacionais forem úteis; eventos separados com retenção definida. Nunca persistir senhas no RunResult ou no log. Não adicionar esses dados às tabelas/resultados do benchmark.

## Injeção e testes

`runner.New(config, factory)` aceita uma factory opcional que retorna Transport. A interface pública possui Start(ctx, handler, notices), Publish(ctx, topic, payload, qos) e Close(ctx). Receipt informa ACK favorável e reason code quando disponível. A implementação deve respeitar cancelamento, permitir Publish concorrente para telemetria/respostas e impedir callbacks após Close retornar. Os testes do runner usam fakes com erros, timeout e comandos para validar comportamento sem rede.

Mantenha o simulador como um módulo funcional separado do mqttbench. O provisionamento futuro por API administrativa pode ser outra etapa/serviço; a biblioteca atual não cria identidades, regras, usuários nem credenciais.

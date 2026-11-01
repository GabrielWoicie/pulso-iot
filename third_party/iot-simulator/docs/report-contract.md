# Contrato do relatório — versão MVP 1.0

Os timestamps são UTC em RFC3339, com frações opcionais. UUID v4 identifica execução e mensagens. A versão do executável identifica este contrato; o JSON não é um resultado científico do benchmark.

## RunResult

| Campo | Interpretação |
|---|---|
| run_id | ID único da execução |
| status | completed, failed ou cancelled no retorno final |
| started_at / finished_at | Início e fim incluindo conexão e limpeza |
| requested_devices | Quantidade selecionada |
| messages_per_device | Tentativas previstas por dispositivo; 0 = contínuo sem limite |
| response_qos | QoS das respostas, independente do QoS de telemetria |
| reason | Motivo de falha/cancelamento, quando aplicável |
| devices | Resultados em ordem de configuração; IDs selecionados e parâmetros efetivos |

## DeviceResult

| Campo | Interpretação |
|---|---|
| device_id / qos / interval_ms | Identidade e parâmetros efetivos de telemetria |
| planned | Tentativas previstas; 0 no contínuo |
| attempted | Chamadas iniciadas de publicação de telemetria |
| published | Chamadas de publicação concluídas sem erro |
| failed | Chamadas com erro, incluindo timeout/cancelamento |
| broker_acknowledged | Publicações concluídas com ACK favorável para QoS 1/2; sempre 0 em QoS 0 |
| ack_reason_codes | Histograma dos reason codes finais de respostas MQTT de telemetria que a biblioteca expôs (chaves decimais) |
| commands_received | Comandos recebidos, incluindo rejeitados na entrada/fila |
| commands_rejected | JSON/contrato inválidos, tipos não suportados, retidos, oversize, overflow ou pendentes ao encerrar |
| responses_published | Respostas concluídas sem erro; inclui executed, unsupported e invalid |
| responses_acknowledged | Respostas com ACK favorável em QoS 1/2 |
| responses_failed | Tentativas de resposta com erro |
| connection_state | Estado final closed; eventos expõem conectado/reconectando durante execução |
| connections | Quantidade de conexões estabelecidas (inclui reconexões) |
| errors | Até 100 primeiros erros sanitizados por dispositivo |
| errors_total | Quantidade total de erros registrados, inclusive os não armazenados no array |
| fatal | Falha que impediu inicialização ou encerramento adequado |

Invariantes na conclusão: `attempted = published + failed`; `broker_acknowledged <= published`; `attempted <= planned` no finito. Se a conexão inicial falhar, há zero tentativas e `fatal=true`. Não converter `planned-attempted` em perdas de rede. `interval_ms` usa truncamento para milissegundos; para a futura API recomenda-se intervalo mínimo de 1ms.

`published` em QoS 0 significa que a biblioteca conseguiu escrever o pacote no socket; não há ACK MQTT. Em QoS 1/2, significa que a chamada bloqueante do protocolo terminou sem erro; ACK favorável inclui código 16 (sem assinantes). Para QoS 2, o reason code normal pode vir de PUBCOMP; rejeição pode vir de PUBREC. O histograma não é contagem de todos os pacotes intermediários e não representa ACK de consumidores.

Timeout/falha de publicação pode deixar entrega indeterminada: a mensagem pode ter sido transmitida antes do erro. Não fazemos retry de aplicação. Nenhum contador comprova entrega ou execução no consumidor, unicidade ponta a ponta, latência, capacidade, taxa de perda ou vazão de benchmark.

`completed` exige ausência de falhas fatais, de telemetria e de publicação de resposta. Comandos inválidos ou não suportados, devidamente rejeitados, não tornam sozinhos a execução `failed`. Eventos transitórios de conexão também não tornam a execução falha se a prontidão for recuperada sem falhas das operações contadas. Contexto cancelado prevalece como `cancelled`, com contadores parciais e motivo.

Exemplo estrutural ilustrativo, sem representar medição executada:

```json
{
  "run_id": "9b9c240e-452a-4ad0-8edc-60bd66d22864",
  "status": "completed",
  "started_at": "2026-10-28T01:00:00Z",
  "finished_at": "2026-10-28T01:00:22Z",
  "requested_devices": 1,
  "messages_per_device": 20,
  "response_qos": 1,
  "devices": [{
    "device_id": "dev-001", "qos": 1, "interval_ms": 1000,
    "planned": 20, "attempted": 20, "published": 20,
    "broker_acknowledged": 20, "ack_reason_codes": {"0": 20}, "failed": 0,
    "commands_received": 1, "commands_rejected": 0,
    "responses_published": 1, "responses_acknowledged": 1, "responses_failed": 0,
    "connection_state": "closed", "connections": 1,
    "errors": [], "errors_total": 0, "fatal": false
  }]
}
```

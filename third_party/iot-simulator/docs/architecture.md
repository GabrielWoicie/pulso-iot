# Arquitetura do simulador

## Componentes

| Pacote | Responsabilidade |
|---|---|
| `cmd/tcc-simulator` | Contexto de sinais, entrada do processo e código de saída |
| `internal/cli` | Flags, resumo no terminal e gravação JSON |
| `pkg/config` | YAML estrito, ambiente, valores padrão e validação |
| `pkg/runner` | API reutilizável, seleção de dispositivos, orquestração, eventos e resultados |
| `internal/mqtt` | Adapter TLS para Eclipse Paho v0.23.0 + autopaho; interface substituível em testes |
| `internal/device` | Tópicos oficiais, processamento de comandos e respostas |
| `internal/telemetry` | Gerador de sensores independente de MQTT |
| `internal/identity` | UUID v4 com fonte criptográfica |

Os pacotes `pkg/config` e `pkg/runner` são exportáveis: um backend em outro módulo Go pode importá-los. A interface Transport também é exposta pelo runner por aliases públicos, sem exigir import de `internal`. O módulo `github.com/woicie/iot-simulator` é um identificador para imports; esta entrega não cria nem publica um repositório GitHub.

## Fluxos

| Canal | Operação do dispositivo | Conteúdo |
|---|---|---|
| `desbravatech/v1/devices/{id}/telemetry` | Publicar, Retain false, QoS efetivo do dispositivo | UUID, timestamp UTC, temperature_c, humidity_pct |
| `desbravatech/v1/devices/{id}/commands` | Assinar QoS solicitado 1 | command_id, type, issued_at opcional; alias action |
| `desbravatech/v1/devices/{id}/command-responses` | Publicar, Retain false, response_qos global | command_id, status, timestamp UTC |

Não existe publicação do simulador em `commands`, tópico de status, Will, filtro global ou controle administrativo do broker. O adapter impede publicação fora de telemetria/respostas do próprio dispositivo.

## Concorrência e ciclo de vida

O runner valida e copia a configuração, seleciona dispositivos e lança uma goroutine por dispositivo. Cada dispositivo possui cliente MQTT próprio, gerador de sensores próprio, um loop serial de telemetria e um worker serial de comandos. Os callbacks MQTT enfileiram comandos em canal limitado a 64 itens; não executam lógica de publicação no callback de recepção. Mensagens maiores que 64 KiB são rejeitadas; overflow de fila é contado e registrado. Contadores compartilhados usam mutex. Erros armazenados são limitados aos primeiros 100 por dispositivo, com `errors_total` separado.

Eventos são entregues de forma serializada ao Sink fornecido. O Sink deve retornar rapidamente (por exemplo, encaminhando para canal com buffer e política explícita de overflow); um consumidor lento pode atrasar a simulação. Não execute nova execução recursivamente dentro do Sink. Os eventos separam dados operacionais dos resultados; o runner não importa flags nem escreve no terminal. O retorno síncrono de `Run` é o resultado final. A documentação de integração mostra como guardá-lo e consultar pelo backend.

Cada dispositivo conecta e confirma sua assinatura antes de iniciar a telemetria. A primeira tentativa é imediata; tentativas posteriores esperam o intervalo configurado após a tentativa anterior. Não há scheduler de carga nem estimativa de latência ponta a ponta. Mensagens tentadas = chamadas de Publish, incluindo falhas. Conexão inicial que nunca fica pronta causa erro fatal com zero chamadas de publicação. Não há barreira de início simultâneo entre dispositivos.

No modo finito, os dispositivos que terminam cedo continuam aceitando comandos até todos finalizarem a telemetria; então aguardam `grace_period`. Cancelamento interrompe timers, espera de conexão e operações MQTT pelo contexto. Ao terminar, o contexto por dispositivo é cancelado, o worker de comandos é aguardado e o cliente é fechado dentro de `shutdown_timeout`, usando contexto próprio de limpeza. Comandos que ainda estiverem na fila ao fechar são contados como rejeitados. Uma resposta em andamento interrompida pode ser registrada como falha. Esse grace period é uma janela prática, não garantia de processar comandos que cheguem no último instante.

## Conexão e reconexão

A biblioteca escolhida é **github.com/eclipse/paho.golang v0.23.0**, cliente Eclipse Paho para **MQTT V5**, com autopaho para reconexão. Ela é distinta de `paho.mqtt.golang`, cliente de outra geração do protocolo. Compatibilidade foi verificada na documentação/código oficial e em integração local MQTT 5. Dependências diretas adicionais: `gopkg.in/yaml.v3 v3.0.1`. WebSocket e x/net são dependências transitivas do autopaho; o simulador usa apenas TLS/TCP.

TLS usa a cadeia confiável do sistema, hostname/SNI da URL e versão mínima TLS 1.2. Não há flag para desativar verificação. O dial de TLS e escritas de socket têm prazos. O callback ConnectPacketBuilder força Clean Start true e Session Expiry 0 **em toda conexão**, pois o padrão do autopaho aplicaria Clean Start somente na inicial. Não há comandos enfileirados de sessões antigas.

Backoff: tentativa inicial imediata, depois 1s, 2s, 4s etc., limitado a `reconnect_max` (mínimo 1s, padrão 30s). Limite inicial de prontidão `connect_timeout`; cada conexão/assinatura usa `operation_timeout`. Após desconexão, o estado vira `reconnecting`; o autopaho tenta reconectar enquanto a execução estiver ativa. Toda conexão refaz a assinatura; telemetria aguarda prontidão dentro do timeout da operação. SUBACK recusado causa falha, em vez de marcar o dispositivo pronto indevidamente.

Não há fila persistente de telemetria nem retry de aplicação da tentativa que falhou. A biblioteca gerencia o protocolo de QoS; uma operação que expira pode ainda chegar ao broker. Reconexão com sessão limpa pode perder operações em trânsito. O relatório não resolve resultados indeterminados; tampouco afirma ausência de duplicação ponta a ponta.

## Comandos e sensores

`ping` é simulado e sem efeitos externos. Nunca executa comando de sistema. `type` é oficial, `action` é alias legado; valores conflitantes resultam em `invalid`. JSON inválido ou command_id ausente/inválido não tem resposta correlacionável; contabiliza rejeição e registra erro. Com ID válido, comandos desconhecidos respondem `unsupported` e problemas de conteúdo respondem `invalid`. Só `ping` válido gera `executed`. `issued_at`, se presente, deve ser RFC3339; ausência é aceita para compatibilidade com ensaios anteriores. Não há validação de idade nem deduplicação de command_id no MVP; repetições de ping são inofensivas e geram novas respostas. Não extrapolar esse comportamento para atuadores futuros.

Sensores usam passeio aleatório limitado: início no centro da faixa, passo máximo de 2,5% da amplitude por amostra, clamp aos limites. Uma semente global é combinada com hash estável do ID; cada dispositivo tem seu PRNG, sem dependência da ordem das goroutines. UUIDs e timestamps são independentes da semente. Geradores são usados por uma goroutine; não são objetos globais concorrentes.

## Fontes técnicas

- https://github.com/eclipse-paho/paho.golang/tree/v0.23.0
- https://pkg.go.dev/github.com/eclipse/paho.golang/autopaho
- https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html
- https://pkg.go.dev/crypto/tls
- https://pkg.go.dev/gopkg.in/yaml.v3

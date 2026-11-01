# Validação manual — EMQX e MQTTX

## Preparação

Estes casos devem ser executados pelo usuário na infraestrutura já provisionada, após a extração do ZIP. **Não foram executados contra o EMQX remoto nesta entrega.** Não usar o mqttbench, não alterar ACL nem criar identidades para rodar o simulador.

Registre data/hora e fuso, versão do simulador (`--version`), Go quando aplicável, Windows/Linux, versão do EMQX, parâmetros sem senha e ID de execução. Mantenha a comparação EMQX × Mosquitto encerrada e separada desses testes funcionais.

1. Copie config.example.yaml para config.yaml e configure as duas variáveis de senha conforme o README.
2. Feche conexões MQTTX com identidades dev-001/dev-002 antes de iniciar os simuladores.
3. Conecte MQTTX como `svc-backend`, MQTT 5.0, `broker.woicie.com.br:8883`, SSL/TLS e verificação de certificado ativados, senha própria do serviço. Use Client ID exatamente `svc-backend`; username pode ser svc-backend, mas o EMQX autentica por clientid.
4. Assine **antes** de publicar comandos, QoS 1:
   - `desbravatech/v1/devices/+/telemetry`
   - `desbravatech/v1/devices/+/command-responses`
5. Não exponha senhas em prints. Os arquivos de configuração locais, .env, chaves, cookies e credenciais não devem integrar evidências versionadas.

## Casos

### SIM-01 — Conexão individual

```powershell
.\dist\tcc-simulator-windows-amd64.exe --config .\config.yaml
```

Esperado: dev-001 e dev-002 mostram conexão e assinatura de commands. Ambos permanecem ativos com Client IDs individuais e autenticados. No Dashboard, se você quiser consultar manualmente, os Client IDs devem aparecer separados. O simulador não acessa esse Dashboard.

Falhas: CONNACK 134 sugere senha/identidade; conexão continuamente substituída sugere outro cliente usando o mesmo ID. Timeout de prontidão pode ser rede, TLS, DNS ou assinatura recusada. Registrar eventos anteriores ao timeout.

Evidência: console sem credenciais e lista de conexões sanitizada, com horário.

### SIM-02 — Telemetria

No MQTTX conectado como svc-backend, observar o filtro `desbravatech/v1/devices/+/telemetry`.

Esperado: mensagens dos dois IDs em seus próprios tópicos, `message_id` distintos, `timestamp` UTC atual, números JSON de temperatura/umidade dentro dos limites. Frequência aproximada conforme intervalos e tempo da operação MQTT; Retain false. A sequência dos sensores não deve apresentar saltos maiores que 2,5% da amplitude por nova amostra. Não usar a frequência observada para conclusões de benchmark.

Evidência: amostras de cada tópico e correspondência com log. UUID não comprova recebimento: o recebimento é verificado no próprio consumidor MQTTX.

### SIM-03 — Comando ping

Mantenha o simulador ativo. No MQTTX como svc-backend, publique com QoS 1 e Retain false:

Tópico: `desbravatech/v1/devices/dev-001/commands`

```json
{"command_id":"cmd-001","type":"ping","issued_at":"2026-10-28T01:00:00Z"}
```

`issued_at` pode ser atualizado para a hora real; o simulador valida o formato e não a idade. Esperado: dev-001 registra processamento, sem executar comando de sistema. dev-002 não recebe esse tópico.

### SIM-04 — Resposta correlacionada

No filtro `desbravatech/v1/devices/+/command-responses`, esperar:

```json
{"command_id":"cmd-001","status":"executed","timestamp":"<UTC RFC3339>"}
```

Tópico deve ser `desbravatech/v1/devices/dev-001/command-responses`, Retain false. O timestamp será real. Essa resposta automatizada comprova o processamento simulado do ping; não existe hardware ou firmware nesta fase.

Evidência: publicação do comando, log de processamento e resposta recebida, com o mesmo command_id. Repita com ID distinto para dev-002 para verificar isolamento.

### SIM-05 — Execução finita

Interrompa a execução anterior com Ctrl+C e inicie:

```powershell
.\dist\tcc-simulator-windows-amd64.exe run --config .\config.yaml --devices dev-001,dev-002 --messages 20 --interval 1s --qos 1 --output-json .\results.json
$LASTEXITCODE
```

Esperado em condições normais: status completed, código 0, dois DeviceResult, cada um com planned=20, attempted=20, published=20, failed=0 e broker_acknowledged=20. Publique um ping enquanto roda para obter contadores de comandos/respostas. Depois do último envio de todos, há grace_period (2s por padrão), seguido do fechamento das conexões. `connection_state` final é closed. Compare recebimentos no MQTTX separadamente; não deduza entrega do JSON.

Repita selecionando apenas dev-001; dev-002 não deve conectar. Teste `--qos 0`: broker_acknowledged deve ser zero mesmo com published positivo. Para QoS 2, valide também os payloads no MQTTX; o QoS de chegada ao assinante depende do QoS solicitado na assinatura.

### SIM-06 — Falhas e recuperação

| Caso | Ação local controlada | Esperado |
|---|---|---|
| Senha incorreta | Altere temporariamente apenas a variável do dev-001 no terminal | CONNACK de recusa; tentativas de reconexão até connect_timeout; resultado failed, fatal=true, zero tentativas de telemetria desse dispositivo. dev-002 pode concluir normalmente. Restaure a variável. |
| Broker indisponível | Use uma cópia do YAML com host reservado `unavailable.invalid:8883` e connect_timeout curto | Sem bypass de TLS; erro de conexão e saída 1 após prazo inicial; nenhuma publicação inventada. |
| Queda de rede | Após conectar, desative a rede local brevemente e restaure, com tempo para recuperação | Estado de reconexão e nova assinatura ao recuperar; operações que expirarem aparecem como falhas; o runner não oculta erros anteriores. |
| Ctrl+C | Interrompa uma execução finita | JSON parcial, status cancelled, reason preenchido e saída 130. Tentativas feitas preservadas. |
| Prazo global | Execute run com `--messages 100 --interval 1s --timeout 3s` | Cancelled, motivo de deadline, resultado parcial, saída 124. |

Uma desconexão pode levar até a detecção do keepalive (30s) para aparecer, especialmente em rede silenciosamente indisponível. Os timeouts de operações também ajudam a detectá-la. O tempo de reconexão depende da rede, DNS e backoff. Em QoS 1/2 uma publicação expirada pode já ter chegado; registre essa incerteza.

### SIM-07 — Segurança do cliente

- O YAML com `mqtt://...:1883` deve ser recusado antes de conectar (saída 2).
- `client_id` diferente do id deve ser recusado. IDs com `/`, `+`, `#`, espaços, maiúsculas ou duplicados devem ser recusados.
- Inspecione logs/relatórios e confirme ausência de senhas. Não coloque credenciais na URL.
- O código configura validação de cadeia e hostname, sem flag de bypass. Certificado inválido ou hostname diferente deve impedir TLS. Não altere o certificado remoto para testar: o teste automatizado local cobre hostname incorreto.
- O simulador só assina commands próprio e só publica telemetry/command-responses próprios. Não há fluxo de administração. Validação das ACLs do broker contra clientes maliciosos continua sendo o escopo dos ensaios do documento 09; este simulador não tenta burlar ACL.
- Não mude AWS/EMQX nem o Security Group para rodar estes casos.

### SIM-08 — Comandos malformados e compatibilidade

Publique como svc-backend, sempre Retain false e em commands do dev-001:

| Payload | Esperado |
|---|---|
| `{"command_id":"alias-001","action":"ping"}` | executed, compatibilidade com testes antigos |
| `{"command_id":"unknown-001","type":"reboot"}` | unsupported, sem reinício |
| `{"command_id":"invalid-001"}` | invalid |
| `{"command_id":"time-001","type":"ping","issued_at":"ontem"}` | invalid |
| `{"type":"ping"}` | rejeição sem resposta correlacionável |
| `{` | rejeição JSON, sem derrubar o processo |

O contador commands_rejected inclui essas rejeições, que por si só não tornam a execução failed. Cada resposta publicada aparece em responses_published, independentemente do status.

## Coleta de evidências do TCC

Sugestão de nomes: `SIM-01-conexao.png`, `SIM-02-telemetria-dev001.png`, `SIM-03-ping-publicado.png`, `SIM-04-resposta.png`, `SIM-05-results.json`, `SIM-06-cancelamento.json`. Inclua uma planilha/tabela de execução com ID, data/hora/fuso, parâmetro, resultado esperado, resultado observado, arquivo de evidência e observações. Não marque caso como aprovado antes de executar.

As capturas devem ocultar senhas e informações privadas desnecessárias. Preserve a versão do executável (e hashes de dist/SHA256SUMS.txt) com o relatório. Não inclua as duas senhas no relatório acadêmico.

## Limites das conclusões

Estes casos validam fluxos, contrato, configuração e comportamento funcional de um número pequeno de dispositivos. Não medem latência ponta a ponta, escalabilidade, taxa de perda, capacidade de EC2 ou segurança formal. Não somar execuções deste simulador às 48 execuções oficiais do benchmark. A integração automatizada local usa Mosquitto apenas para testar o cliente; compatibilidade operacional final com seu EMQX e certificados públicos requer os casos acima.

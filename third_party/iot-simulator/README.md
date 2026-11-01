# iot-simulator — tcc-simulator 1.0.0

Simulador funcional de dispositivos IoT para o TCC 2 da UNOESC: **Projeto e avaliação de desempenho de uma plataforma IoT baseada em MQTT com EMQX**. Cada dispositivo usa conexão MQTT 5.0, credencial e Client ID próprios, transmite temperatura/umidade, recebe `ping` e responde pelo MQTT.

Os testes deste projeto são funcionais. O benchmark EMQX × Mosquitto está encerrado, permanece independente e não é executado nem alterado por este programa.

## Primeiro uso no Windows 11 (PowerShell)

Extraia o ZIP e abra o PowerShell **na pasta `iot-simulator`**, onde está este README. Os binários fornecidos em `dist/` são para Windows x64 e Linux amd64. Não é necessário instalar Go para usar o executável pronto.

```powershell
Copy-Item .\config.example.yaml .\config.yaml
```

Configure as duas senhas já cadastradas no EMQX. As linhas abaixo solicitam a senha sem exibi-la ou incluí-la no histórico do terminal:

```powershell
$env:DEV_001_PASSWORD = [System.Net.NetworkCredential]::new('', (Read-Host 'Senha MQTT dev-001' -AsSecureString)).Password
$env:DEV_002_PASSWORD = [System.Net.NetworkCredential]::new('', (Read-Host 'Senha MQTT dev-002' -AsSecureString)).Password
.\dist\tcc-simulator-windows-amd64.exe --config .\config.yaml
```

Essas variáveis valem para a sessão atual do PowerShell. A aplicação lê `${DEV_001_PASSWORD}` e `${DEV_002_PASSWORD}` do ambiente. `.env.example` é apenas referência: **não existe carregamento automático de `.env`**. O arquivo YAML não precisa conter a senha. Após usar, `Remove-Item Env:DEV_001_PASSWORD, Env:DEV_002_PASSWORD` remove as variáveis desta sessão.

No MQTTX, mantenha uma conexão separada como `svc-backend`: Client ID `svc-backend`, senha desse serviço, MQTT 5.0, host `broker.woicie.com.br`, porta `8883`, SSL/TLS ativado e validação de certificado ativada. Use o armazenamento de CAs do sistema. Assine os dois filtros:

```text
desbravatech/v1/devices/+/telemetry
desbravatech/v1/devices/+/command-responses
```

Publique em `desbravatech/v1/devices/dev-001/commands`, QoS 1, Retain desativado:

```json
{"command_id":"cmd-001","type":"ping","issued_at":"2026-10-28T01:00:00Z"}
```

Deve chegar uma resposta com `command_id=cmd-001` e `status=executed`. `issued_at` é opcional; quando informado, deve ser RFC3339. O simulador não impõe limite de idade ao comando. Gere IDs diferentes para novas solicitações. **Não mantenha conexões MQTTX com Client IDs `dev-001` ou `dev-002` enquanto os simuladores usam essas identidades**, pois o broker pode substituir a conexão anterior. Encerre com Ctrl+C.

## Execução finita

No mesmo PowerShell, com as variáveis ainda definidas:

```powershell
.\dist\tcc-simulator-windows-amd64.exe run --config .\config.yaml --devices dev-001,dev-002 --messages 20 --interval 1s --qos 1 --output-json .\results.json
$LASTEXITCODE
Get-Content .\results.json
```

São **20 tentativas por dispositivo**, total previsto de 40. O primeiro envio é imediato depois da assinatura; cada próximo envio espera o intervalo após o término da tentativa anterior. O tempo total inclui conexão, operações MQTT e finalização; não é um gerador de carga com cadência científica. Durante o período ativo, os dispositivos continuam processando comandos. Depois que todos terminam a telemetria, há um período extra de 2s por padrão, alterável com `--grace-period 5s`.

O relatório JSON usa QoS e intervalo efetivos por dispositivo. Veja o contrato em [docs/report-contract.md](docs/report-contract.md). Publicação concluída ou confirmação do broker **não comprova recebimento por consumidor**. QoS 0 nunca incrementa `broker_acknowledged`.

## Linux

```bash
cp config.example.yaml config.yaml
read -r -s -p 'Senha dev-001: ' DEV_001_PASSWORD; echo
read -r -s -p 'Senha dev-002: ' DEV_002_PASSWORD; echo
export DEV_001_PASSWORD DEV_002_PASSWORD
chmod +x dist/tcc-simulator-linux-amd64
./dist/tcc-simulator-linux-amd64 --config config.yaml
# Ou:
./dist/tcc-simulator-linux-amd64 run --config config.yaml --messages 20 --interval 1s --qos 1 --output-json results.json
unset DEV_001_PASSWORD DEV_002_PASSWORD
```

## Requisitos e compilação

Para desenvolver: Go **1.24 ou superior** (exigência do Eclipse Paho v0.23.0); instale a versão estável disponível em https://go.dev/dl/ e confirme `go version`. No Windows, o instalador MSI configura o PATH; abra um terminal novo. No Linux, siga https://go.dev/doc/install. O projeto não usa CGO, Node, Docker, banco ou backend HTTP.

```bash
go mod tidy
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/tcc-simulator
go run ./cmd/tcc-simulator --help
```

`-race` requer um ambiente Go com suporte ao detector de corridas (em certas plataformas, compilador C). A compilação normal do simulador não precisa de CGO.

Cross-compilation no Linux:

```bash
mkdir -p dist
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/tcc-simulator-windows-amd64.exe ./cmd/tcc-simulator
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/tcc-simulator-linux-amd64 ./cmd/tcc-simulator
```

Compilação no Windows, PowerShell:

```powershell
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED='0'
$env:GOOS='windows'; $env:GOARCH='amd64'
go build -trimpath -ldflags='-s -w' -o .\dist\tcc-simulator-windows-amd64.exe .\cmd\tcc-simulator
$env:GOOS='linux'
go build -trimpath -ldflags='-s -w' -o .\dist\tcc-simulator-linux-amd64 .\cmd\tcc-simulator
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Para desenvolver no VS Code, instale a extensão Go recomendada. Abra o VS Code pelo PowerShell com as variáveis definidas (`code .`) para que as configurações de depuração herdem as credenciais.

## CLI

`run` é opcional: sem ele, modo contínuo. Use flags depois do subcomando.

| Flag | Padrão | Uso |
|---|---|---|
| `--config` | `config.yaml` | Configuração YAML |
| `--devices` | todos | Lista de IDs separados por vírgula |
| `--messages` | 20 em `run` | Tentativas por dispositivo; inteiro positivo; somente `run` |
| `--interval` | configuração | Intervalo positivo (`1s`, `500ms`) |
| `--qos` | configuração | QoS da telemetria: 0, 1 ou 2 |
| `--grace-period` | YAML: 2s | Tempo extra ao final da telemetria; zero permitido |
| `--timeout` | 0 | Limite total; zero = ilimitado |
| `--output-json` | desativado | Salva relatório final, inclusive parcial em cancelamento |
| `--help`, `-h` | — | Ajuda sem conectar |
| `--version` | — | Versão sem conectar |

QoS de respostas é `response_qos`, global no YAML, padrão 1, independente de `--qos`. A assinatura de comandos solicita QoS 1. Flags substituem valores somente em memória; não alteram o YAML. `--timeout` interrompe também o período extra. Logs operacionais vão para stderr; resumo vai para stdout; JSON vai para o arquivo solicitado. Uma pasta de saída deve existir previamente.

Códigos de saída: `0` concluído (ou Ctrl+C intencional no contínuo), `1` falha MQTT/relatório, `2` uso/configuração inválidos, `124` timeout global, `130` Ctrl+C durante execução finita. Mesmo quando Ctrl+C retorna 0 no contínuo, seu relatório registra `cancelled`. Em falha de conexão inicial, não inventamos tentativas: o dispositivo fica com `fatal=true`, zero tentativas e o motivo em `errors`.

## Configuração e segurança

O exemplo completo está em [config.example.yaml](config.example.yaml). `client_id` e `username` são opcionais: padrões `id` e `client_id`. Neste MVP, um Client ID diferente de `id` é rejeitado, pois as identidades e ACLs provisionadas exigem igualdade. Autenticação no EMQX é por Client ID; username é enviado, mas não substitui essa identidade.

- Apenas `mqtts://hostname:8883`, sem credenciais na URL, caminho ou parâmetros. Sem opção de TLS inseguro; SNI vem do hostname e a cadeia usa CAs confiáveis do sistema. TLS mínimo 1.2. Não é necessário instalar CA própria para Let's Encrypt.
- MQTT 5.0, Clean Start **em toda conexão**, Session Expiry = 0, sem Will nem tópicos extras. Nova assinatura em cada reconexão; comandos retidos são ignorados e a assinatura pede para não receber retidos.
- Dispositivos com IDs duplicados ou fora de `^[a-z0-9]+(?:-[a-z0-9]+)*$` são rejeitados. QoS 0–2, intervalos positivos, limites finitos/ordenados, umidade 0–100%, temperatura dentro de -100 a 150°C. Defina intervalos de sensores plausíveis para o seu cenário.
- Preencha os limites em `telemetry` como no exemplo. Campo numérico omitido é zero em Go; não há inferência de faixa ausente. Por exemplo, faixa `{min: 0, max: 0}` representa sensor constante em zero.
- `seed` reproduz valores e sequência por dispositivo independentemente da ordem de execução; IDs de mensagem e horários continuam reais e únicos.
- `${VAR_NAME}` é resolvido em escalares após a leitura da estrutura YAML, preservando pontuação de senhas. Variável ausente ou vazia causa erro. Campos desconhecidos, aliases, tipos inválidos e múltiplos documentos são rejeitados.
- Nenhuma credencial real é fornecida. Senhas não são registradas; erros de parsing omitem o conteúdo YAML e erros do runner passam por remoção das senhas configuradas. Não há API administrativa, criação de usuários, alteração de ACL ou acesso ao Dashboard.

Padrões globais: `qos: 1`, `response_qos: 1`, `interval: 5s`, `connect_timeout: 20s`, `operation_timeout: 10s`, `shutdown_timeout: 5s`, `reconnect_max: 30s`, `grace_period: 2s`. QoS/intervalo por dispositivo prevalecem sobre globais. `interval` omitido ou zero no dispositivo herda o global. A tentativa inicial de conexão é imediata; reconexões usam backoff 1s, 2s, 4s etc., limitado por `reconnect_max`. Depois de pronto, reconecta enquanto o contexto da execução estiver ativo.

## Saídas e verificação

Exemplo ilustrativo de log (horários/valores mudam; não é evidência de teste no EMQX):

```text
2026-10-28T01:00:00Z [dev-001] conectado ao broker
2026-10-28T01:00:00Z [dev-001] inscrito em commands
2026-10-28T01:00:00Z [dev-001] telemetry -> temperature_c=25.08 humidity_pct=57.74
2026-10-28T01:00:01Z [dev-001] comando recebido e processado
2026-10-28T01:00:01Z [dev-001] command-response publicado: executed
```

Os registros reais das verificações locais estão em `docs/verification.txt` e `docs/integration-verification.txt`. Veja [docs/manual-validation.md](docs/manual-validation.md) para validar seu EMQX usando MQTTX; os testes reais na sua infraestrutura não foram executados nesta entrega.

Teste opcional de integração local, Linux, com `mosquitto` e `mosquitto_passwd` instalados:

```bash
MQTT_TEST_BROKER_PATH=/usr/sbin/mosquitto go test -race -tags integration -v ./internal/mqtt
```

Esse teste sobe um processo local em `127.0.0.1:8883`, gera certificado confiável **somente para o processo de teste**, senhas e ACL temporários, verifica QoS 0/1/2, comandos/respostas, senha incorreta, hostname incorreto e reinício do broker com nova assinatura. A porta deve estar livre. Sem a variável, o teste opcional é marcado como skipped. O teste não modifica um broker instalado e remove arquivos temporários, incluindo chaves. Certificados privados de teste nunca entram no ZIP.

## Problemas frequentes

| Sintoma | Verificação |
|---|---|
| Variável obrigatória ausente | Defina ambas no mesmo terminal que abre o programa. `.env` não é lido. |
| Conexão não ficou pronta | Consulte mensagens anteriores; confirme DNS atual, firewall 8883, rede, hora do Windows e certificado. Limite inicial padrão: 20s. |
| CONNACK 134 | Senha incorreta ou identidade não reconhecida; confira Client ID cadastrado no banco interno. |
| Conecta/desconecta repetidamente | Outro MQTTX/processo pode estar usando o mesmo Client ID; confirme também rede e keepalive. |
| Assinatura recusada | ACL precisa permitir o tópico `commands` do próprio dispositivo. |
| Publicação falha / código 135 | ACL negou; confira credencial, identidade e tópico. QoS 0 pode não fornecer negação observável ao publicador. |
| Confirmação com código 16 | `No matching subscribers`: sucesso protocolar sem consumidor correspondente; abra a assinatura `svc-backend`. |
| Não aparece telemetria no MQTTX | Confirme filtros, Client ID `svc-backend`, ACL e TLS. ACK do simulador não garante recebimento no MQTTX. |
| Comando não recebe resposta | Assine respostas antes de enviar, desative Retain, use JSON e command_id válidos, mantenha o simulador ativo. |
| Erro TLS | Corrija relógio/CA do sistema/DNS/certificado. Nunca desative validação; redes com inspeção TLS podem exigir análise local. |
| Erro ao salvar JSON | Diretório deve existir e ser gravável; destino não pode ser a configuração. |

Mais detalhes: [arquitetura](docs/architecture.md), [integração futura](docs/future-integration.md), [contrato do relatório](docs/report-contract.md), [rastreabilidade](docs/source-decisions.md).

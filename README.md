# pulso-iot

## 1. Antes de executar

- Go 1.24 ou superior; PostgreSQL com o schema `pulso_iot` do anexo.
- EMQX **Community 5.8.9**: autenticação password-based/built-in por **clientid**, autorização built-in habilitada, `no_match=deny`, fontes permissivas desabilitadas.
- API Key/Secret administrativos do EMQX; não usar login do painel como Basic Auth.
- Identidade MQTT `svc-backend`, sem superuser, e senha própria. ACL: subscribe `desbravatech/v1/devices/+/telemetry`, subscribe `.../+/command-responses`, publish `.../+/commands`. Nunca conceder publish de telemetria ao backend.
- MQTT por `mqtts://hostname:8883`, com certificado e hostname válidos. API EMQX por HTTPS, ou HTTP somente em loopback.
- API Pulso atrás de Nginx HTTPS (exemplo em `deploy/`). HTTP local é o upstream interno do proxy.

**Não execute o baseline sobre o banco já instalado.** O arquivo original se chama v1.0, mas contém o baseline v1.1 com ciphertext. A cópia em `db/001_baseline.sql` é idêntica ao anexo. Aplique a migracao incremental `db/002_device_mode.sql` antes de iniciar esta versao. Leia `db/README.md` para instalação nova e permissões mínimas.

## 2. Configuração e primeiro administrador

Linux/macOS, a partir da raiz do projeto:

```bash
cp .env.example .env
# Edite .env com suas credenciais; proteja com chmod 600 .env.
# Para a chave de credenciais use: openssl rand -base64 32
# Para JWT_SECRET use: openssl rand -hex 32
# Use aspas para valores com espaços, # ou $; aspas simples preservam literais.
go mod download
go build -buildvcs=false -trimpath -o bin/pulso-iot ./cmd/pulso-iot
./bin/pulso-iot bootstrap-admin
# Após o bootstrap, remova ADMIN_NAME/ADMIN_EMAIL/ADMIN_PASSWORD do .env.
./bin/pulso-iot check-broker
./bin/pulso-iot
```

`bootstrap-admin` lê `ADMIN_NAME`, `ADMIN_EMAIL`, `ADMIN_PASSWORD`. Exige senha de 12–72 bytes com letras, números e símbolo. Recusa se já existir administrador ativo. Não imprime senha, token ou hash. O programa **carrega `.env` automaticamente da pasta de execução**, antes de ler e validar as configurações. Isso funciona no servidor, `bootstrap-admin` e `check-broker`. Não é necessário usar `source`, `set -a` ou exportar cada variável. Variáveis já definidas no processo (inclusive vazias) têm prioridade; o arquivo só preenche as ausentes. Arquivo ausente é permitido quando as configurações vêm do ambiente, como no systemd/Docker; arquivo inválido ou ilegível interrompe a inicialização com erro sanitizado. Nenhuma pasta pai ou pasta do executável é pesquisada. O arquivo é lido na inicialização, sem recarga automática quando editado. `keygen` imprime um segredo aleatório URL-safe para JWT; para `MQTT_CREDENTIAL_KEY_B64` use **base64 padrão** (`openssl rand -base64 32`).

No Windows/PowerShell, use `Copy-Item .env.example .env`, preencha o arquivo na raiz do projeto, compile com `go build -buildvcs=false -o bin/pulso-iot.exe ./cmd/pulso-iot` e execute `.\bin\pulso-iot.exe` a partir dessa raiz. O `.env` é carregado sem scripts de importação. Também é possível definir `$env:NOME='valor'` para sobrepor um valor do arquivo. Não envie variáveis administrativas ao frontend.

O servidor inicia em `127.0.0.1:8080`. Falha na validação administrativa do broker impede a inicialização; perda posterior de MQTT resulta em health 503 e reconexão automática. O comando `check-broker` só faz leituras; não cria nem muda autenticação global, API keys ou a conta técnica.

## 3. Usar a API

Todos os endpoints estão em `docs/api.md`, com uma especificação OpenAPI em `docs/openapi.json`. Há requisições prontas para REST Client em `docs/requests.http`.

```bash
curl -s http://127.0.0.1:8080/api/v1/health
curl -s http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"seu-email","password":"sua-senha"}'
```

Use `data.access_token` no header `Authorization: Bearer ...`. JWT dura 15 minutos; expirou, faça login novamente. Papel/status são consultados no banco em cada requisição. Desativação é imediata para requisições seguintes. Não há refresh/logout/revogação individual de token; reset de senha não invalida tokens já emitidos, que expiram no máximo em 15 minutos. O último admin ativo não pode ser removido/rebaixado/desativado, inclusive sob concorrência.

O login limita 10 tentativas por endereço de conexão em 10 minutos. Headers X-Forwarded-For não são confiados; atrás de um único proxy o limite será compartilhado pelos usuários. Para escala maior, implemente limitação também no proxy e revise os valores. Não há controle de sessão por IP.

Dispositivos usam UUID nas rotas REST, mas `device_identifier` (`dev-001`) na seleção de testes e nos tópicos. Identidade é imutável. `status` indica administração; `online` é consulta à sessão EMQX e pode ser `null` se indisponível.

## 4. Fluxo funcional completo

1. Faça login como admin/operator e `POST /devices` com nome, identificador, tipo, QoS e intervalo. O backend grava credencial cifrada e aplica autenticação/ACL ao EMQX. Senha é retornada em `mqtt_password_once`, só nesta operação.
2. Se ocorrer 503, verifique o corpo: o cadastro pode já estar salvo e conter seu UUID. Consulte o dispositivo e use `POST /devices/{uuid}/reconcile`. Não repita cadastro cegamente.
3. Para os dispositivos já importados no banco sem ciphertext, desconecte MQTTX/hardware e use **admin** `POST /devices/{uuid}/credentials/import` com a senha **atual**. O backend confirma um CONNECT MQTT 5/TLS com essa senha antes de salvá-la e reconciliar. Não existe GET de senha.
4. Cadastre os simuladores com `device_mode="simulated"`; o nome nao define a classificacao. Não conecte hardware/MQTTX com esses IDs durante os testes. A API verifica sessão online antes de começar, mas não consegue impedir que um cliente externo conecte depois.
5. `POST /tests` com `{"device_ids":["dev-001","dev-002"],"messages":20,"interval_ms":1000,"qos":1}` retorna **202** com UUID. O runner roda em memória no mesmo processo; não gera YAML ou arquivos com senhas. Consulte `GET /tests/{uuid}` até terminar.
6. Enquanto o simulador está conectado, envie `POST /devices/{uuid}/commands` com `{"type":"ping"}`. Consulte `GET /commands/{uuid-do-comando}` até `executed`. PUBACK/publicação não significa execução; resposta correlacionada é necessária.
7. Telemetria e respostas aparecem em `/messages`, `/devices/{uuid}/telemetry` e no dashboard. Mensagens têm horários de envio/recebimento. Latência, throughput e contagem de recebimento **por teste** permanecem `null`, pois não são instrumentados; não são inventados a partir de PUBACK.

Dispositivos sem ciphertext, inativos, removidos, classificados como physical ou conectados são recusados para testes. Uma execução por vez; até 10 dispositivos, 1000 mensagens por dispositivo, intervalo de 100–60000 ms e até 15 minutos de publicação (mais 45s de margem). Durante execução, edição/rotação/remoção desses dispositivos retorna 409; ping permanece permitido. Cancelar solicita interrupção e preserva contadores parciais.

## 5. Falhas, recuperação e limites

- PostgreSQL e EMQX não compartilham transação. A intenção fica persistida antes da operação externa; `pending/error` e `last_error` sanitizado tornam falhas reconciliáveis. `active/revoked` só após a API administrativa confirmar os passos.
- Reconciliação é explícita e repetível; não há fila de retry automático do provisionamento. Uma identidade preexistente nunca tem sua senha substituída por criação/reconciliação sem histórico de provisionamento. Use importação validada ou rotação explícita.
- Remoção preserva histórico, revoga usuário/ACL e encerra sessão. Em falha, retorna 503; o broker pode ainda aceitar autenticação até reconciliar.
- Conexão MQTT tem retry/reassinatura. Falha/recusa de assinatura deixa `mqtt=false` no health. A fila de ingestão tem 1024 posições, 4 workers, payload até 64 KiB e timeout DB de 5s. Saturação é contada em `messages_dropped`; falha de persistência em `persistence_failures`. Não há replay durável local: confirmação MQTT não garante gravação no banco. Deduplicação de telemetria usa `(device_id,message_id)`; sem message_id, duplicatas são possíveis.
- Payload de telemetria exige timestamp RFC3339, temperature_c em -100..150 e humidity_pct em 0..100; device_id, se presente, deve coincidir com tópico. O simulador atual não envia device_id: a identidade vem do tópico autorizado. Retain é sempre rejeitado.
- Comandos sem resposta expiram em 30s. Em timeout de publicação, a entrega pode ser indeterminada; o histórico indica falha e não há reenvio automático.
- Ao reiniciar, testes `queued/running` viram `failed`/`PROCESS_INTERRUPTED`; resultados podem estar incompletos. Escrita final usa transação. Se o banco cair nessa etapa, logs apontam falha de persistência e recuperação ocorrerá ao reiniciar.
- Apenas **uma instância** por banco: advisory lock mantém a propriedade dos workers. Perda da conexão do lock encerra o processo. Não usar PgBouncer em transaction pooling para essa conexão; use PostgreSQL direto ou session pooling.
- Segredos ficam em configuração e memória; backups da chave exigem proteção. Key version 1 implementada; troca da chave exige plano de recriptografia/rotação, não basta substituir variável.

## 6. Testes e build

```bash
go test -race ./...
go vet ./...
(cd third_party/iot-simulator && go test -race ./...)
go build -buildvcs=false ./cmd/pulso-iot
```

Integração: use **banco vazio descartável**, jamais o banco do TCC. A suíte recusa schema existente, cria o baseline e remove somente o schema criado pelo teste.

```bash
export PULSO_TEST_DATABASE_URL='postgres://usuario:senha@127.0.0.1:55432/pulso_testasslmode=disable'
# Mosquitto e mosquitto_passwd locais habilitam o cenário de rede TLS.
export MQTT_TEST_BROKER_PATH=/usr/sbin/mosquitto
go test -race -tags integration -v ./...
```

A suíte TLS gera certificado temporário e credenciais de teste, inicia broker só em loopback 8883 e o encerra depois. Recusa porta ocupada. Para distribuição Linux, instale `mosquitto` e `mosquitto-clients` conforme sua distribuição. A fixture herdada do simulador usa `user root` somente no broker de teste; rode em ambiente descartável/container. Não use essa configuração no broker de produção.

Os testes da API administrativa EMQX usam `httptest` com contrato conferido no código da tag 5.8.9. O cenário MQTT real usa Mosquitto local, não valida a configuração da sua instância EMQX. Veja evidências e limitações em `docs/VALIDACAO.md`.

## 7. Organização

- `cmd/pulso-iot`: servidor, bootstrap e preflight.
- `internal/app`: REST/RBAC, regras, SQL pgxpool, consumidor MQTT, comandos e runner.
- `internal/emqx`: cliente REST administrativo, provisionamento/revogação idempotentes.
- `third_party/iot-simulator`: fonte do simulador anexado e testes próprios.
- `db`: baseline original e instruções de permissões.
- `deploy`: Dockerfile na raiz, Nginx e systemd como exemplos de implantação.
- `docs`: contrato REST/OpenAPI, manual de validação e integração do frontend.

Os 48 ensaios científicos do mqttbench e a planilha master permanecem fora deste backend. Nenhuma rotina os importa ou altera.

### Reproduzir a verificação embarcada usada nesta entrega (opcional)

No ambiente da entrega, PostgreSQL nativo não pôde ser iniciado por restrição de usuário do container. O fallback de teste é PostgreSQL compilado para WebAssembly via PGlite; não é dependência da aplicação e não substitui a validação em PostgreSQL nativo antes de produção.

Com Node 20+, Go e, opcionalmente, Mosquitto:

```bash
cd scripts/embedded-tests
npm ci
# MQTT_TEST_BROKER_PATH habilita o teste de rede; sem ela esse cenário é SKIP.
node run.mjs
```

O script inicia banco vazio em loopback 55432, usa pool de uma conexão (restrição da fixture), executa os testes com race detector e encerra o banco. Recusa porta ocupada. Não é servidor de desenvolvimento/produção.

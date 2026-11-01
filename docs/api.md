# Contrato REST v1

Base `/api/v1`. Todos os corpos são objetos JSON; campos desconhecidos são recusados. Rotas `{id}` recebem **UUID**, inclusive dispositivos. A exceção é `POST /tests.device_ids`, que recebe os identificadores MQTT textuais.

Login e health são públicos. Demais rotas usam `Authorization: Bearer <JWT>`.

| Permissão | Papéis |
|---|---|
| leitura | admin, operator, viewer |
| operação | admin, operator |
| administração | admin |

## Rotas

| Método e caminho | Permissão | Entrada / retorno |
|---|---|---|
| POST `/auth/login` | pública | email, password → access_token, token_type, expires_in |
| GET `/auth/me` | leitura | usuário atual sem hash |
| GET `/users` | administração | filtros search, role, status |
| POST `/users` | administração | name, email, password, role, status |
| GET `/users/{id}` | administração | detalhes |
| PATCH `/users/{id}` | administração | name, email, role, status (parciais) |
| PATCH `/users/{id}/password` | administração | password |
| DELETE `/users/{id}` | administração | desativa; preserva histórico |
| GET `/devices` | leitura | filtros search, status, type; sort=created_at/name/device_identifier |
| POST `/devices` | operação | name, device_identifier, device_type, description, default_qos, publish_interval_ms, mqtt_password opcional |
| GET `/devices/{id}` | leitura | cadastro, provisionamento, tópicos, online EMQX ou null |
| PATCH `/devices/{id}` | operação | name, device_type, description, default_qos, publish_interval_ms, status=active/inactive |
| DELETE `/devices/{id}` | administração | remoção lógica + revogação MQTT + kick |
| GET `/devices/{id}/topics` | leitura | array dos três tópicos oficiais |
| POST `/devices/{id}/credentials/rotate` | operação | `{}` gera senha ou `{mqtt_password}`; versão incrementada |
| POST `/devices/{id}/credentials/import` | administração | mqtt_password atual; confirma CONNECT TLS antes de salvar |
| POST `/devices/{id}/reconcile` | operação | sem corpo; reaplica estado desejado, inclusive para removidos |
| POST `/devices/{id}/provision` | operação | alias de reconcile |
| GET `/messages` | leitura | filtros device_id UUID, kind, date_from, date_to |
| GET `/devices/{id}/messages` | leitura | histórico dos três canais; mesmos filtros de período |
| GET `/devices/{id}/telemetry` | leitura | somente telemetria; filtros de período |
| POST `/devices/{id}/commands` | operação | `{type:"ping"}`; action é alias compatível |
| GET `/devices/{id}/commands` | leitura | histórico de comandos |
| GET `/commands/{id}` | leitura | status, request_payload e response_payload |
| POST `/tests` | operação | device_ids textuais, messages, interval_ms, qos |
| GET `/tests` | leitura | filtro status |
| GET `/tests/{id}` | leitura | execução, summary, results por dispositivo |
| POST `/tests/{id}/cancel` | operação | sem corpo; 202 cancellation_requested |
| GET `/dashboard/summary` | leitura | cadastros, ativos, mensagens de 24h, testes, últimos comandos; sessões globais do broker ou null |
| GET `/dashboard/recent-messages` | leitura | histórico paginado recente; aceita limit |
| GET `/dashboard/message-volume` | leitura | period=1h/24h/7d; buckets horários inclusive vazios |
| GET `/broker/status` | leitura | preflight e estado MQTT; 503 quando incompatível/indisponível |
| GET `/audit-logs` | administração | filtros action, entity_type |
| GET `/health` | pública | database, mqtt, time, contadores de ingestão; 503 degradado |

## Envelopes e paginação

Listagens de usuários, dispositivos, mensagens, comandos, testes e auditoria:

```json
{"data":[],"pagination":{"currentPage":1,"totalPages":0,"totalCount":0,"limit":20}}
```

`page`: 1..1000000. `limit`: 1..100, padrão 20. Filtros combinam com AND. search usa ILIKE em nome/email ou nome/identificador. Datas exigem RFC3339 com fuso, por exemplo `2026-10-31T00:00:00-03:00`. Ordem temporal decrescente, com chave de desempate. Buckets de volume e tópicos retornam arrays sem paginação, pois são limitados e derivados.

Detalhes: `{"data":{...}}`. Erro: `{"error":{"code":"...","message":"..."}}`. Criação de usuário/dispositivo/comando usa 201; execução/cancelamento usa 202. Cadastro de dispositivo com falha de broker usa 503 mas inclui `data.id` e `mqtt_password_once`, pois a intenção e o segredo já foram persistidos.

| HTTP | Uso |
|---|---|
| 400 | JSON desconhecido/inválido, UUID, período, paginação |
| 401 | JWT inválido/expirado, usuário inativo, login incorreto |
| 403 | papel sem permissão |
| 404 | recurso não encontrado/removido |
| 409 | duplicidade, último admin, cliente conectado, teste/reserva em andamento |
| 422 | validação de campos, comando não suportado, credencial/dispositivo inelegível |
| 429 | limite de tentativas de login |
| 503 | banco, MQTT, broker ou provisionamento indisponível/incompleto |

## Credenciais e provisionamento

Não há GET de senha. GET de dispositivo pode incluir `has_mqtt_password`, `credential_version`, datas, status e `last_error` sanitizado, nunca hash/ciphertext. `mqtt_password_once` só aparece em criação e rotação. A importação não ecoa a senha. Proteja inclusive logs do cliente que usa a API.

Tópicos fixos por identidade:

| Canal | Permissão do dispositivo |
|---|---|
| `desbravatech/v1/devices/{identifier}/telemetry` | publish |
| `desbravatech/v1/devices/{identifier}/commands` | subscribe |
| `desbravatech/v1/devices/{identifier}/command-responses` | publish |

Provisionamento é repetível com estado persistido, mas a primeira tentativa não sobrescreve identidades preexistentes. Para DB importado sem credenciais, usar importação validada. `provisioning_status`: pending, active, error, revoked. DB+broker não são atômicos; erro não desfaz o cadastro.

## Payloads MQTT

Telemetria compatível com o simulador anexado:

```json
{"message_id":"uuid-ou-identificador","timestamp":"2026-10-31T12:00:00Z","temperature_c":25.4,"humidity_pct":61.2}
```

Comando emitido pelo backend:

```json
{"command_id":"uuid","type":"ping","issued_at":"2026-10-31T12:00:00Z"}
```

Resposta:

```json
{"command_id":"mesmo-uuid","status":"executed","timestamp":"2026-10-31T12:00:00Z"}
```

Somente executed/unsupported/failed são respostas aceitas. Nenhum comando de SO é executado. Respostas tardias ficam no histórico de mensagens, mas não substituem estados finais/timeout. PUBACK != execução != persistência != recebimento por todos os consumidores.

## Classificacao administrativa de dispositivos

Aplicar uma vez, com a aplicacao parada e a role de migracao (faca backup antes):

```powershell
psql "$env:DATABASE_URL_ADMIN" -v ON_ERROR_STOP=1 -f db/002_device_mode.sql
```

Instalacao nova: aplicar 001 e depois 002. Banco existente: somente 002. A migracao nao recria tabelas: mantem UUIDs, identidades, topicos, credenciais, mensagens, auditoria e historicos. Todos os registros existentes recebem physical, inclusive nomes dev-*. Revise o inventario e classifique explicitamente os virtuais via PATCH autenticado; nao deduza pelo nome.

POST /api/v1/devices aceita device_mode: physical (padrao se omitido) ou simulated. PATCH /api/v1/devices/{uuid} preserva o modo quando omitido. null, vazio, tipos incorretos e valores desconhecidos retornam 422 INVALID_DEVICE_MODE. Listagem, detalhe e cadastro retornam device_mode. GET /api/v1/devices?device_mode=simulated&page=1&limit=20 combina o filtro com os demais; filtro invalido retorna 400.

Exemplos para o frontend:

```json
{"name":"Sensor virtual","device_identifier":"sensor-001","device_type":"sensor","device_mode":"simulated"}
```

```json
{"device_mode":"physical"}
```

```json
{"device_ids":["sensor-001"],"messages":20,"interval_ms":1000,"qos":1}
```

A classificacao e declaracao administrativa, nao deteccao automatica de hardware. device_type permanece o tipo funcional. Alterar somente o modo nao muda identidade/senha nem reprovisiona. Mudanca efetiva exige desconexao (409 CLIENT_CONNECTED), consulta EMQX disponivel (503 BROKER_UNAVAILABLE) e ausencia de reserva (409 DEVICE_BUSY); e serializada com o inicio dos testes e auditada sem segredos. Reenviar o mesmo modo nao exige desconexao. admin/operator editam; viewer consulta.

O runner exige simulated persistido: sensor-001 simulado e elegivel, dev-001 fisico recebe 422 DEVICE_NOT_SIMULATED. Selecao mista falha sem criar execucao, reservar ou iniciar clientes. Permanecem as demais verificacoes de prontidao, credenciais, sessoes, limites e cancelamento. TEST_DEVICE_PREFIX antigo e ignorado. Fisicos continuam publicando telemetria e recebendo ping.

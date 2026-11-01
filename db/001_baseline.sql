-- ============================================================================
-- PULSO IOT / TCC 2 - schema completo BASELINE v1.1
-- Banco de destino: postgres (ja existente)
-- Schema criado: pulso_iot
-- Compatibilidade: PostgreSQL 13+ (gen_random_uuid nativo)
-- Fonte: TCC 1 (DER e requisitos); 08-arquitetura-seguranca-mqtt.md;
--        09-resultados-seguranca-mqtt.md; prototipo pulso-iot-admin.
-- Executar no Query Tool do pgAdmin conectado ao banco postgres, ou via psql.
-- SCRIPT BASELINE PARA INSTALACAO NOVA: NAO executar novamente no schema existente.
-- NAO executa DROP, nao cria senhas, nao modifica EMQX, nao importa benchmark.
-- Inclui desde a instalacao inicial as colunas da migracao 04 (v1.1).
-- Para bancos ja criados com v1.0 e migrados por 04, NAO executar este baseline.
-- ============================================================================

BEGIN;

CREATE SCHEMA pulso_iot;

-- 1) Administradores do painel; hash bcrypt/Argon2 gerado pelo backend Go.
CREATE TABLE pulso_iot.users (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           VARCHAR(150) NOT NULL,
    email          VARCHAR(255) NOT NULL,
    password_hash  TEXT NOT NULL,
    role           VARCHAR(20) NOT NULL DEFAULT 'viewer',
    status         VARCHAR(20) NOT NULL DEFAULT 'active',
    last_login_at  TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT users_email_not_blank CHECK (length(btrim(email)) > 3),
    CONSTRAINT users_role_check CHECK (role IN ('admin', 'operator', 'viewer')),
    CONSTRAINT users_status_check CHECK (status IN ('active', 'inactive'))
);
CREATE UNIQUE INDEX ux_pulso_users_email_lower
    ON pulso_iot.users (lower(email));

-- 2) Cadastro administrativo. client_id = device_identifier no MVP:
--    autenticacao no EMQX por clientid; identidade estavel, sem caracteres MQTT.
--    Status e administrativo, nao representa conexao online em tempo real.
CREATE TABLE pulso_iot.devices (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 VARCHAR(150) NOT NULL,
    device_identifier    VARCHAR(100) NOT NULL UNIQUE,
    device_type          VARCHAR(40) NOT NULL DEFAULT 'sensor',
    client_id            VARCHAR(100) NOT NULL UNIQUE,
    mqtt_username        VARCHAR(100) NOT NULL,
    status               VARCHAR(20) NOT NULL DEFAULT 'inactive',
    description          TEXT,
    default_qos          SMALLINT NOT NULL DEFAULT 1,
    publish_interval_ms  INTEGER NOT NULL DEFAULT 5000,
    last_seen_at         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at           TIMESTAMPTZ,
    CONSTRAINT devices_identifier_format CHECK (
        device_identifier ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
    ),
    CONSTRAINT devices_clientid_matches_identifier CHECK (client_id = device_identifier),
    CONSTRAINT devices_status_check CHECK (status IN ('active', 'inactive', 'testing')),
    CONSTRAINT devices_qos_check CHECK (default_qos BETWEEN 0 AND 2),
    CONSTRAINT devices_interval_check CHECK (publish_interval_ms > 0),
    CONSTRAINT devices_name_not_blank CHECK (length(btrim(name)) > 0)
);
CREATE INDEX ix_pulso_devices_status
    ON pulso_iot.devices (status) WHERE deleted_at IS NULL;
CREATE INDEX ix_pulso_devices_last_seen
    ON pulso_iot.devices (last_seen_at DESC);

-- 3) Identidade MQTT e senha CIFRADA recuperavel apenas pelo backend.
--    EMQX continua verificando a senha por hash; aqui usa-se AES-256-GCM no Go.
--    Formato BYTEA = nonce aleatorio de 12B + ciphertext + tag GCM de 16B.
--    Chave de 32B fica no .env/secret manager do backend, NUNCA no PostgreSQL.
--    AAD: pulso_iot:mqtt_credentials:v1:<UUID device_id>.
CREATE TABLE pulso_iot.mqtt_credentials (
    device_id             UUID PRIMARY KEY REFERENCES pulso_iot.devices(id) ON DELETE RESTRICT,
    authenticator         VARCHAR(100) NOT NULL DEFAULT 'password_based:built_in_database',
    provisioning_status   VARCHAR(20) NOT NULL DEFAULT 'pending',
    credential_version    INTEGER NOT NULL DEFAULT 1,
    provisioned_at        TIMESTAMPTZ,
    rotated_at            TIMESTAMPTZ,
    revoked_at            TIMESTAMPTZ,
    last_error            TEXT,
    password_ciphertext   BYTEA,
    password_key_version  SMALLINT NOT NULL DEFAULT 1,
    password_saved_at     TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT mqtt_credentials_status_check CHECK (
        provisioning_status IN ('pending', 'active', 'error', 'revoked')
    ),
    CONSTRAINT mqtt_credentials_version_check CHECK (credential_version > 0),
    CONSTRAINT mqtt_credentials_password_version_check CHECK (password_key_version > 0),
    CONSTRAINT mqtt_credentials_password_state_check CHECK (
        (password_ciphertext IS NULL AND password_saved_at IS NULL)
        OR
        (password_ciphertext IS NOT NULL AND password_saved_at IS NOT NULL
         AND octet_length(password_ciphertext) >= 29)
    )
);

-- 4) Espelho administrativo das ACLs; a autorizacao efetiva vive no EMQX.
--    Um registro para cada um dos 3 canais oficiais por dispositivo.
CREATE TABLE pulso_iot.device_topics (
    id               BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    device_id        UUID NOT NULL REFERENCES pulso_iot.devices(id) ON DELETE CASCADE,
    topic_kind       VARCHAR(25) NOT NULL,
    topic            TEXT NOT NULL,
    permission_type  VARCHAR(12) NOT NULL,
    qos_default      SMALLINT NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT device_topics_kind_check CHECK (
        topic_kind IN ('telemetry', 'commands', 'command-responses')
    ),
    CONSTRAINT device_topics_permission_check CHECK (
        (topic_kind = 'commands' AND permission_type = 'subscribe') OR
        (topic_kind IN ('telemetry', 'command-responses') AND permission_type = 'publish')
    ),
    CONSTRAINT device_topics_qos_check CHECK (qos_default BETWEEN 0 AND 2),
    CONSTRAINT device_topics_unique_kind UNIQUE (device_id, topic_kind),
    CONSTRAINT device_topics_unique_topic UNIQUE (device_id, topic, permission_type)
);

-- 5) Execucoes FUNCIONAIS do simulator (futura tela /testes).
--    NAO armazenar aqui resultados do benchmark mqttbench, que ja foi encerrado.
CREATE TABLE pulso_iot.test_runs (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 VARCHAR(150),
    scenario             VARCHAR(100) NOT NULL DEFAULT 'simulator-functional',
    status               VARCHAR(20) NOT NULL DEFAULT 'queued',
    selected_device_ids  TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    messages_per_device  INTEGER NOT NULL,
    interval_ms          INTEGER NOT NULL,
    qos                  SMALLINT NOT NULL DEFAULT 1,
    summary              JSONB NOT NULL DEFAULT '{}'::JSONB,
    error_message        TEXT,
    started_at           TIMESTAMPTZ,
    finished_at          TIMESTAMPTZ,
    created_by           UUID REFERENCES pulso_iot.users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT test_runs_status_check CHECK (
        status IN ('queued', 'running', 'completed', 'failed', 'cancelled')
    ),
    CONSTRAINT test_runs_messages_check CHECK (messages_per_device > 0),
    CONSTRAINT test_runs_interval_check CHECK (interval_ms > 0),
    CONSTRAINT test_runs_qos_check CHECK (qos BETWEEN 0 AND 2),
    CONSTRAINT test_runs_summary_object CHECK (jsonb_typeof(summary) = 'object'),
    CONSTRAINT test_runs_dates_check CHECK (
        finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at
    )
);
CREATE INDEX ix_pulso_test_runs_created_at
    ON pulso_iot.test_runs (created_at DESC);
CREATE INDEX ix_pulso_test_runs_status
    ON pulso_iot.test_runs (status, created_at DESC);

-- 6) Resultado por dispositivo, importado pelo backend Go do runner.
--    received_by_backend e metricas de latencia podem ser NULL quando NAO medidas.
CREATE TABLE pulso_iot.test_results (
    id                     BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    test_run_id            UUID NOT NULL REFERENCES pulso_iot.test_runs(id) ON DELETE CASCADE,
    device_id              UUID NOT NULL REFERENCES pulso_iot.devices(id) ON DELETE RESTRICT,
    status                 VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempted              INTEGER NOT NULL DEFAULT 0,
    published              INTEGER NOT NULL DEFAULT 0,
    acknowledged_by_broker INTEGER NOT NULL DEFAULT 0,
    received_by_backend    INTEGER,
    failed                 INTEGER NOT NULL DEFAULT 0,
    commands_received      INTEGER NOT NULL DEFAULT 0,
    responses_published    INTEGER NOT NULL DEFAULT 0,
    avg_latency_ms         NUMERIC(12,3),
    p95_latency_ms         NUMERIC(12,3),
    throughput_msg_s       NUMERIC(14,3),
    details                JSONB NOT NULL DEFAULT '{}'::JSONB,
    started_at             TIMESTAMPTZ,
    finished_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT test_results_one_per_device UNIQUE (test_run_id, device_id),
    CONSTRAINT test_results_status_check CHECK (
        status IN ('pending', 'running', 'completed', 'failed', 'cancelled')
    ),
    CONSTRAINT test_results_counts_check CHECK (
        attempted >= 0 AND published >= 0 AND acknowledged_by_broker >= 0 AND
        (received_by_backend IS NULL OR received_by_backend >= 0) AND
        failed >= 0 AND commands_received >= 0 AND responses_published >= 0
    ),
    CONSTRAINT test_results_metrics_check CHECK (
        (avg_latency_ms IS NULL OR avg_latency_ms >= 0) AND
        (p95_latency_ms IS NULL OR p95_latency_ms >= 0) AND
        (throughput_msg_s IS NULL OR throughput_msg_s >= 0)
    ),
    CONSTRAINT test_results_details_object CHECK (jsonb_typeof(details) = 'object')
);
CREATE INDEX ix_pulso_test_results_device
    ON pulso_iot.test_results (device_id, created_at DESC);

-- 7) Comandos backend -> dispositivo e sua resposta correlacionada por command_id.
CREATE TABLE pulso_iot.device_commands (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id         UUID NOT NULL REFERENCES pulso_iot.devices(id) ON DELETE RESTRICT,
    command_id        VARCHAR(120) NOT NULL,
    command_type      VARCHAR(50) NOT NULL DEFAULT 'ping',
    request_payload   JSONB NOT NULL,
    response_payload  JSONB,
    status            VARCHAR(20) NOT NULL DEFAULT 'pending',
    issued_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at      TIMESTAMPTZ,
    responded_at      TIMESTAMPTZ,
    error_message     TEXT,
    test_run_id       UUID REFERENCES pulso_iot.test_runs(id) ON DELETE SET NULL,
    created_by        UUID REFERENCES pulso_iot.users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT device_commands_unique_command UNIQUE (device_id, command_id),
    CONSTRAINT device_commands_status_check CHECK (
        status IN ('pending', 'published', 'executed', 'unsupported', 'failed', 'timeout', 'cancelled')
    ),
    CONSTRAINT device_commands_payload_check CHECK (
        jsonb_typeof(request_payload) = 'object' AND
        (response_payload IS NULL OR jsonb_typeof(response_payload) = 'object')
    )
);
CREATE INDEX ix_pulso_commands_device
    ON pulso_iot.device_commands (device_id, issued_at DESC);
CREATE INDEX ix_pulso_commands_status
    ON pulso_iot.device_commands (status, issued_at DESC);

-- 8) Registro dos 3 tipos de mensagens oficiais.
--    Backend subscriber salva telemetry/command-responses; backend publisher
--    pode registrar commands. Recebimento backend != entrega a todos os destinos.
CREATE TABLE pulso_iot.mqtt_messages (
    id              BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    device_id       UUID NOT NULL REFERENCES pulso_iot.devices(id) ON DELETE RESTRICT,
    test_run_id     UUID REFERENCES pulso_iot.test_runs(id) ON DELETE SET NULL,
    topic           TEXT NOT NULL,
    message_kind    VARCHAR(25) NOT NULL,
    direction       VARCHAR(10) NOT NULL,
    message_id      VARCHAR(120),
    command_id      VARCHAR(120),
    payload         JSONB NOT NULL,
    qos             SMALLINT NOT NULL,
    retain          BOOLEAN NOT NULL DEFAULT FALSE,
    sent_at         TIMESTAMPTZ,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    latency_ms      NUMERIC(12,3),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT mqtt_messages_kind_check CHECK (
        message_kind IN ('telemetry', 'commands', 'command-responses')
    ),
    CONSTRAINT mqtt_messages_direction_check CHECK (direction IN ('inbound', 'outbound')),
    CONSTRAINT mqtt_messages_qos_check CHECK (qos BETWEEN 0 AND 2),
    CONSTRAINT mqtt_messages_retain_check CHECK (retain = FALSE),
    CONSTRAINT mqtt_messages_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT mqtt_messages_latency_check CHECK (latency_ms IS NULL OR latency_ms >= 0)
);
CREATE INDEX ix_pulso_messages_device_received
    ON pulso_iot.mqtt_messages (device_id, received_at DESC);
CREATE INDEX ix_pulso_messages_kind_received
    ON pulso_iot.mqtt_messages (message_kind, received_at DESC);
CREATE INDEX ix_pulso_messages_test_run
    ON pulso_iot.mqtt_messages (test_run_id) WHERE test_run_id IS NOT NULL;
CREATE UNIQUE INDEX ux_pulso_telemetry_message_id
    ON pulso_iot.mqtt_messages (device_id, message_id)
    WHERE message_kind = 'telemetry' AND message_id IS NOT NULL;

-- 9) Auditoria administrativa; NAO registrar segredos no JSON details.
CREATE TABLE pulso_iot.audit_logs (
    id             BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    actor_user_id  UUID REFERENCES pulso_iot.users(id) ON DELETE SET NULL,
    actor_type     VARCHAR(20) NOT NULL DEFAULT 'user',
    action         VARCHAR(100) NOT NULL,
    entity_type    VARCHAR(80) NOT NULL,
    entity_id      UUID,
    details        JSONB NOT NULL DEFAULT '{}'::JSONB,
    occurred_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT audit_logs_actor_type_check CHECK (actor_type IN ('user', 'system')),
    CONSTRAINT audit_logs_details_object CHECK (jsonb_typeof(details) = 'object')
);
CREATE INDEX ix_pulso_audit_logs_occurred
    ON pulso_iot.audit_logs (occurred_at DESC);
CREATE INDEX ix_pulso_audit_logs_entity
    ON pulso_iot.audit_logs (entity_type, entity_id, occurred_at DESC);

-- Atualizacao automatica de updated_at (apenas entidades alteraveis).
CREATE OR REPLACE FUNCTION pulso_iot.touch_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$function$;

CREATE TRIGGER tr_users_updated_at BEFORE UPDATE ON pulso_iot.users
FOR EACH ROW EXECUTE FUNCTION pulso_iot.touch_updated_at();

CREATE TRIGGER tr_devices_updated_at BEFORE UPDATE ON pulso_iot.devices
FOR EACH ROW EXECUTE FUNCTION pulso_iot.touch_updated_at();

CREATE TRIGGER tr_mqtt_credentials_updated_at BEFORE UPDATE ON pulso_iot.mqtt_credentials
FOR EACH ROW EXECUTE FUNCTION pulso_iot.touch_updated_at();

CREATE TRIGGER tr_test_runs_updated_at BEFORE UPDATE ON pulso_iot.test_runs
FOR EACH ROW EXECUTE FUNCTION pulso_iot.touch_updated_at();

CREATE TRIGGER tr_device_commands_updated_at BEFORE UPDATE ON pulso_iot.device_commands
FOR EACH ROW EXECUTE FUNCTION pulso_iot.touch_updated_at();

COMMENT ON SCHEMA pulso_iot IS 'Plataforma IoT TCC 2: Go + PostgreSQL + EMQX; sem dados do benchmark mqttbench.';
COMMENT ON TABLE pulso_iot.mqtt_credentials IS 'Metadados e senha MQTT cifrada AES-256-GCM recuperavel APENAS pelo backend Go; a autenticacao via hash permanece no EMQX.';
COMMENT ON COLUMN pulso_iot.mqtt_credentials.password_ciphertext IS 'Envelope [nonce 12B][AES-256-GCM ciphertext + tag 16B]. AAD inclui device_id. Nunca salvar texto puro ou chave de criptografia neste campo.';
COMMENT ON COLUMN pulso_iot.mqtt_credentials.password_key_version IS 'Versao da chave AES utilizada no backend; inicial 1.';
COMMENT ON COLUMN pulso_iot.mqtt_credentials.password_saved_at IS 'Horario da ultima inclusao/rotacao da senha MQTT cifrada.';
COMMENT ON TABLE pulso_iot.device_topics IS 'Politica administrativa espelhada; autorizacao e aplicada pelo EMQX, nao por esta tabela.';
COMMENT ON TABLE pulso_iot.test_runs IS 'Execucoes funcionais da futura tela /testes; nao corresponde ao benchmark cientifico.';
COMMENT ON COLUMN pulso_iot.test_results.received_by_backend IS 'NULL quando o recebimento pelo backend nao foi medido; diferente de zero.';
COMMENT ON COLUMN pulso_iot.mqtt_messages.latency_ms IS 'Preencher somente com metodologia de medicao valida; clocks distintos podem enviesar o resultado.';

COMMIT;

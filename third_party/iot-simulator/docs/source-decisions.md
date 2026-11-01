# Fontes do projeto e decisões de escopo

Referências obrigatórias lidas antes da implementação:

1. `08-arquitetura-seguranca-mqtt.md`, versão 1.0 de 24/10/2026: contrato dos tópicos, identidade device_id = clientid, ACL por dispositivo, MQTT 5.0, sessão limpa, separação de componentes e benchmark encerrado.
2. `09-resultados-seguranca-mqtt.md`, versão **1.0**, registro de 27/10/2026: EMQX 5.8.9, autenticação por clientid/bcrypt, usuários dev-001/dev-002/svc-backend, ACL efetivas, códigos MQTT observados, três fluxos manuais e TLS válido.
3. Especificação anexada `Texto colado(2).txt`: requisitos desta implementação.

O pedido cita `09-resultados-seguranca-mqtt-atualizado-v1.1.md`, mas o arquivo disponibilizado tem título e metadados de versão 1.0. Não foi fornecida uma versão 1.1 identificável. Isso não impede implementar o simulador, pois os tópicos, identidades, TLS e contratos usados já estão confirmados nos documentos disponíveis.

O pedido informa HTTPS do Dashboard já disponível e porta 1883 bloqueada externamente; o documento 09 v1.0 registra etapas de endurecimento ainda pendentes. Essas informações são registradas como provenientes do pedido, sem afirmar que foram verificadas nesta entrega. O simulador usa exclusivamente MQTT seguro na 8883 e não depende do Dashboard, do listener 1883 ou da API administrativa. Nenhuma configuração AWS/EMQX foi alterada ou inspecionada remotamente.

Decisões do MVP: Client ID alternativo rejeitado; pacotes públicos para reutilização por outro módulo; response_qos global separado do QoS de telemetria; duração native Go na API interna e conversão explícita de milissegundos para futura API HTTP; ping sem efeitos de sistema; comandos sem tipo/tempo válido respondem invalid quando correlacionáveis; tipo desconhecido responde unsupported; JSON/ID inválido sem resposta. Limites de memória: fila de 64 comandos, payload de 64 KiB, 100 erros armazenados por dispositivo. Não há deduplicação de command_id ou persistência.

Nenhum arquivo de benchmark foi alterado ou incluído como dependência. Mosquitto é usado somente como broker local opcional nos testes de integração do simulador. Isso não reabre nem reproduz o benchmark científico.

# Histórico

## 1.0.1 — 31/10/2026

- Carregamento automático de `.env` da pasta de execução antes de validar as configurações no servidor, bootstrap-admin e check-broker.
- Variáveis já definidas no processo têm prioridade, inclusive quando vazias. A ausência de arquivo é permitida para configuração externa (systemd/Docker).
- Sintaxe inválida ou arquivo ilegível interrompe a inicialização sem expor valores do arquivo nos logs. Não há recarga dinâmica nem busca em pastas pai.
- `.env.example` e README atualizados para Linux e PowerShell. Depois do bootstrap, remover ADMIN_* do arquivo; usar apenas `unset` não apaga valores que continuam no `.env`.
- Dependência direta: github.com/joho/godotenv v1.5.1. Licença MIT em docs/godotenv-LICENSE.txt; referência oficial: https://github.com/joho/godotenv.
- Testes de precedência, aspas/CRLF, arquivo ausente, sintaxe inválida e inicialização pelos três comandos. Executáveis Linux/Windows recompilados.

Os contratos REST e o schema permanecem compatíveis com a versão 1.0.0. Não é necessária migração de banco.

## 1.0.0

Entrega inicial do backend e integração com o runner do simulador.

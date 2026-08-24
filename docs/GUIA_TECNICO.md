# MC Manager — Guia Técnico do Backend

> **Repo:** [`lomokwa/mc-manager-server`](https://github.com/lomokwa/mc-manager-server) · Go + [Gin](https://gin-gonic.com/) · SQLite ([`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3), CGO)
> **Repo irmão:** [`lomokwa/mc-manager-client`](https://github.com/lomokwa/mc-manager-client) — o frontend tem seu próprio [`docs/GUIA_TECNICO.md`](https://github.com/lomokwa/mc-manager-client/blob/main/docs/GUIA_TECNICO.md).
> **English:** [`docs/TECHNICAL_GUIDE.md`](TECHNICAL_GUIDE.md)
>
> Este guia descreve o código no commit [`ed608ed`](https://github.com/lomokwa/mc-manager-server/commit/ed608ed). Os links com número de linha podem ficar desatualizados conforme o arquivo muda; se o link cair um pouco fora do lugar, procure pelo nome da função/símbolo indicado ao lado — esse não muda.

## Sumário

1. [Arquitetura do sistema](#1-arquitetura-do-sistema)
2. [Estrutura do repositório](#2-estrutura-do-repositório)
3. [Sequência de boot](#3-sequência-de-boot)
4. [Configuração (variáveis de ambiente)](#4-configuração-variáveis-de-ambiente)
5. [Autenticação e autorização](#5-autenticação-e-autorização)
6. [Referência da API HTTP](#6-referência-da-api-http)
7. [Módulos de domínio](#7-módulos-de-domínio)
8. [Modelo de dados](#8-modelo-de-dados)
9. [Observabilidade](#9-observabilidade)
10. [O protocolo do console (WebSocket)](#10-o-protocolo-do-console-websocket)
11. [Testes](#11-testes)
12. [CI/CD e deploy](#12-cicd-e-deploy)
13. [Frontend, em resumo](#13-frontend-em-resumo)
14. [Receituário — "eu quero…"](#14-receituário--eu-quero)
15. [Glossário](#15-glossário)

---

## 1. Arquitetura do sistema

O MC Manager roda em **dois containers** que compartilham um único diretório montado (`./minecraft-server`) e mais nada — sem canal de rede entre eles:

```
┌─────────────────────────┐     bind mount compartilhado    ┌──────────────────────────┐
│  mc-manager (este repo)  │◄────  ./minecraft-server  ────► │  minecraft (Dockerfile.   │
│  API REST em Go + Gin    │       (world, logs, jar,        │  minecraft, deste mesmo   │
│  :8080                   │       server.properties,        │  repo, cmd/supervisor)    │
│                          │       plano de controle          │  dono direto da JVM       │
└─────────────────────────┘       .mcmanager/)               └──────────────────────────┘
```

Essa separação existe pra que **redeploy da API nunca desconecte jogador**. O caminho de deploy do `docker-compose.yml` só reconstrói o `mc-manager` (`docker compose up -d --build --no-deps mc-manager` — ver [§12](#12-cicd-e-deploy)); o container `minecraft`, e a JVM dentro dele, ficam intocados num deploy normal.

Como a API não tem handle direto sobre a JVM, os dois containers conversam por três arquivos no volume compartilhado — o **plano de controle**, definido em [`services/constants.go`](../services/constants.go) e implementado por [`cmd/supervisor/main.go`](../cmd/supervisor/main.go):

| Arquivo | Direção | Propósito |
|---|---|---|
| `.mcmanager/console.in` (FIFO) | API → supervisor | Comandos de console crus, repassados literalmente pro stdin da JVM |
| `.mcmanager/control.in` (FIFO) | API → supervisor | Verbos de ciclo de vida: `START`, `STOP`, `RESTART`, `KILL` |
| `.mcmanager/status.json` | supervisor → API | Heartbeat trocado atomicamente: `running` / `pid` / `since` / `heartbeat` / `desired` |

A API também não lê o stdout da JVM diretamente — o Minecraft já escreve `logs/latest.log`, e a API acompanha esse arquivo sozinha ([`services/logtail.go`](../services/logtail.go)) em vez de depender do supervisor pra entrega de log. Dois canais independentes, dois modos de falha independentes — mais fácil de raciocinar do que um só.

**Nota sobre multi-servidor.** Neste commit, a tabela `servers` (ver [§8](#8-modelo-de-dados)) e a abstração `ServerRuntime` ([§7.1](#71-ciclo-de-vida-do-servidor--o-registro-multi-servidor)) já suportam *descrever* mais de um servidor, mas só existe uma JVM rodando de fato — não existe `POST /api/servers` ainda, e o `cmd/supervisor` só gerencia um processo por vez. Trate o registro como "Fase 1 de um rollout multi-servidor", não como "multi-servidor já está no ar".

## 2. Estrutura do repositório

```
main.go                    Ponto de entrada: sequência de boot + tabela de rotas (newRouter)
logging.go                 Setup do slog (JSON em produção, texto com GIN_MODE=debug)
main_routes_test.go        Testes de ponta a ponta do router (rotas flat vs. namespaced)
main_redaction_test.go     Trava a redação de credencial no log de acesso

cmd/supervisor/main.go     O OUTRO binário. Roda dentro do container "minecraft",
                            dono da JVM, fala o protocolo do plano de controle acima.
                            Só compila em Linux (//go:build linux).

handlers/                  Um arquivo por recurso REST. Cada função é um
                            gin.HandlerFunc: lê a entrada, chama uma função de
                            services/, monta a resposta JSON. Regra de negócio
                            não mora aqui.
middleware/                gin.HandlerFunc transversais: auth JWT/API-key,
                            checagem de permissão, rate limiting, resolução
                            de :sid.
services/                  Toda a regra de negócio. Fala com o filesystem, com
                            as FIFOs do plano de controle, e com o banco.
types/                     Structs compartilhadas: formato das linhas do banco,
                            corpos de request/response da API, o enum de
                            permissão.
db/                        db.go abre o arquivo SQLite e roda migrations.sql
                            em todo boot (todas as instruções são IF NOT
                            EXISTS — ver §8).
utils/                     Helpers pequenos e sem estado (hoje só file.go).
docs/                      Este guia, seu par em inglês, e docs.go (anotações
                            Swagger geradas — ver §6).
```

Tudo dentro de `web/` é um **resquício solto, não versionado**, de um experimento de frontend standalone que foi rejeitado. Não faz parte do sistema em produção, nenhum Dockerfile referencia aquilo, e não deve ser tratado como documentação de nada.

## 3. Sequência de boot

Lido de cima a baixo, o `main()` de [`main.go`](../main.go) faz exatamente isto, nesta ordem:

1. **[`setupLogging()`](../logging.go)** — instala o handler de `slog` do processo inteiro. Precisa ser o primeiro: tudo depois disso já pode logar.
2. **`godotenv.Load()`** — carrega `.env` na base do melhor esforço; arquivo ausente não é erro (deploy em container injeta env var direto).
3. **listener do pprof** — sobe numa goroutine, vinculado só a `127.0.0.1:6060`. Ver [§9](#9-observabilidade).
4. **`db.Init(os.Getenv("DB_PATH"))`** — abre o SQLite, roda [`db/migrations.sql`](../db/migrations.sql).
5. **[`services.EnsureBuiltinRoles()`](../services/permissions.go#L18)** — semeia as cinco roles nativas (Owner/Admin/Moderator/Operator/Viewer). Fatal se der erro.
6. **[`services.ApplyPermissionsSeed()`](../services/seed.go#L45)** — lê `permissions-seed.json` se existir e atribui role pros usernames que baterem. Roda em todo boot; não faz nada pra quem já tem role. Ver [§5.2](#52-permissões--o-modelo-rbac).
7. **[`services.EnsureBootstrapOwner()`](../services/seed.go#L83)** — rede de segurança: se *ninguém* tiver role depois do passo 6 (seed ausente, ou os usernames dele ainda não se registraram), promove a primeira conta registrada a Owner. Sem isso, um deploy novo sem seed negaria acesso a *todo mundo*, permanentemente, sem porta de volta pela UI.
8. **[`services.EnsureDefaultServer()`](../services/servers.go#L28)** — idempotente: semeia a tabela `servers` com uma linha apontando pro `ServerDir` que já existia, só se a tabela estiver vazia. Fatal se der erro.
9. **[`services.LoadRuntimes()`](../services/runtime.go#L110)** — monta o `map[string]*ServerRuntime` em memória a partir da tabela `servers` e inicia o tailer de log de cada runtime. Precisa rodar antes de qualquer handler ficar acessível.
10. **[`services.StartBackupScheduler()`](../services/backup_scheduler.go#L15)** — inicia a goroutine que roda backups agendados conforme `backup_config`.
11. **`gin.SetMode(...)`** — variável `GIN_MODE`, padrão `release`.
12. **`newRouter().Run()`** — monta a tabela de rotas inteira (ver [§6](#6-referência-da-api-http)) e bloqueia, servindo HTTP.

Os passos 5, 8 e 9 chamam um helper local `fatal()` ([`logging.go`](../logging.go)) em caso de erro — ele loga em `ERROR` pelo handler estruturado e chama `os.Exit(1)`, então uma falha de boot fica tão consultável no seu agregador de log quanto qualquer outra coisa que o processo emite.

## 4. Configuração (variáveis de ambiente)

| Variável | Obrigatória | Padrão | Usada por |
|---|---|---|---|
| `DB_PATH` | sim | — | [`db.Init`](../db/db.go) — caminho do arquivo SQLite |
| `JWT_SECRET` | sim | — | [`middleware/auth.go`](../middleware/auth.go) — chave HMAC pra assinar o token de login |
| `API_KEY` | pra rota admin | — | [`middleware/auth.go`](../middleware/auth.go) — auth via `X-API-Key` / `?key=`, comparada em tempo constante |
| `CORS_ALLOWED_ORIGINS` | não | `http://localhost:5173,http://localhost:8080` | [`main.go` `allowedOrigins()`](../main.go) — separado por vírgula; também controla o check de `Origin` do WebSocket do console (guarda contra CSWSH, [`handlers/console.go`](../handlers/console.go)) |
| `GIN_MODE` | não | `release` | [`main.go`](../main.go) — `debug` também troca o handler de log pra texto (ver [§9](#9-observabilidade)) |
| `LOG_LEVEL` | não | `info` | [`logging.go`](../logging.go) — `debug`\|`info`\|`warn`\|`error`; valor não reconhecido cai silenciosamente pra `info` em vez de recusar o boot |
| `PORT` | não | `8080` (padrão do gin) | o listener HTTP |
| `CLIENT_URL` | conforme o caso | — | referenciada por fluxos de convite/link que montam uma URL de volta pro frontend |

Variáveis só do container (lidas pelo `cmd/supervisor`, não pela API): `MC_SERVER_DIR` (padrão `/mc`), `MC_SERVER_JAR` (padrão `server.jar`), `MC_JAVA_XMS`/`MC_JAVA_XMX` (padrão `1G`/`2G`), `MC_STOP_TIMEOUT` (padrão `30s`) — todas lidas em [`cmd/supervisor/main.go`](../cmd/supervisor/main.go).

## 5. Autenticação e autorização

Três camadas independentes, aplicadas nesta ordem numa requisição típica: **auth** (quem é você) → **permissão** (você pode fazer isso) → **resolução de servidor** (qual servidor, pras rotas namespaced).

### 5.1 Middleware de autenticação

| Middleware | Arquivo | Aceita | Usado em |
|---|---|---|---|
| `ValidateJWT()` | [`middleware/auth.go`](../middleware/auth.go) | header `Authorization: Bearer <jwt>`, ou query param `?token=` (o WebSocket do console precisa disso — browser não seta header no handshake de WS) | o grupo de rotas `api` (praticamente tudo) |
| `ValidateAPIKey()` | [`middleware/auth.go`](../middleware/auth.go) | header `X-API-Key` ou query param `?key=`, comparado com [`crypto/subtle.ConstantTimeCompare`](https://pkg.go.dev/crypto/subtle#ConstantTimeCompare) | não fica sozinho; é combinado no próximo |
| `ValidateAPIKeyOrJWT()` | [`middleware/auth.go`](../middleware/auth.go) | qualquer um dos dois acima | o grupo `admin` (`POST /api/admin/invitations`) — deixa um script criar convite só com a API key, sem login |

Claims do JWT setadas no login ([`services/users.go` `Login`](../services/users.go#L93)): `user_id` (vira `float64` depois de decodificado — número JSON), `username`, `exp` (24h). Lidas de volta via [`middleware.UserIDFromContext`](../middleware/permissions.go#L14) e [`middleware.UsernameFromContext`](../middleware/permissions.go).

Os dois query params `key` e `token` são removidos dos logs de acesso — ver [§9](#9-observabilidade).

### 5.2 Permissões — o modelo RBAC

**Arquivos:** [`types/permissions.go`](../types/permissions.go) (o enum + o schema + as roles nativas), [`services/permissions.go`](../services/permissions.go) (resolução + mutação), [`middleware/permissions.go`](../middleware/permissions.go) (o middleware do gin).

Uma `Permission` é uma string estável (`"server.start"`, `"files.delete"`, …), guardada como JSON em dois lugares:

- `roles.permissions` — o conjunto padrão de uma role
- `user_roles.overrides` — um `map[Permission]bool` aplicado **por cima** do padrão da role, específico de um usuário

[`EffectivePermissions(userID)`](../services/permissions.go#L46) calcula a resposta final: parte da lista da role (tudo `true`), depois aplica os overrides (`true` adiciona, `false` revoga explicitamente algo que a role concederia). **Um usuário sem linha em `user_roles` tem zero permissões — negado por padrão.** Isso não é um estado de erro; é o padrão pra toda conta recém-registrada até um admin atribuir uma role.

`RequirePermission(perm)` ([`middleware/permissions.go`](../middleware/permissions.go)) é o middleware do gin que toda rota protegida usa; em `main.go` recebe o apelido `perm` pra ficar mais curto (`perm(types.PermServerStart)`). Devolve 403 nomeando a permissão que falta no corpo do erro — o `apiFetch` do cliente (ver o guia do frontend) mostra essa mensagem direto, em vez de um "acesso negado" genérico.

**Referência de permissões** — todo valor em `PermissionSchema` de [`types/permissions.go`](../types/permissions.go), agrupado por zona (é exatamente o que `GET /api/permissions/schema` devolve, e o que o editor de role do cliente renderiza):

| Zona | Permissão | Concede |
|---|---|---|
| Controle do servidor | `server.start` | Ligar o servidor Minecraft |
| | `server.stop` | Desligar |
| Console | `console.read` | Ver o feed de console ao vivo |
| | `console.chat` | Transmitir mensagem de chat (`say`) |
| | `console.commands` | Rodar qualquer outro comando de console |
| Arquivos | `files.read` | Navegar e baixar |
| | `files.upload` | Adicionar arquivos novos |
| | `files.edit` | Alterar conteúdo de arquivo existente |
| | `files.delete` | Remover arquivos/pastas |
| Backups | `backups.view` | Ver lista e agendamento |
| | `backups.create` | Backup manual + mudar agendamento |
| | `backups.download` | Baixar um arquivo de backup |
| | `backups.delete` | Remover um arquivo de backup |
| | `backups.restore` | Substituir o mundo ao vivo — a mais destrutiva |
| Configurações | `settings.view` | Ver `server.properties` |
| | `settings.edit` | Mudar `server.properties` |
| Performance | `performance.view` | TPS/memória/CPU |
| | `performance.report` | Rodar profiler spark/relatórios de saúde |
| Jogadores | `players.view` | Lista e perfis |
| | `players.moderate` | Op/de-op, kick, ban, whitelist |
| Administração | `admin.manage_users` | Convidar/remover contas do site |
| | `admin.manage_roles` | Atribuir role, editar overrides por usuário |

**Roles nativas** ([`types.BuiltinRoles`](../types/permissions.go#L158), semeadas por [`EnsureBuiltinRoles`](../services/permissions.go#L18) em todo boot):

| Role | Permissões |
|---|---|
| **Owner** | Toda permissão que existe. Não pode ser atribuída/editada pela API nem pela UI — só pelo arquivo de seed (ver abaixo). Existe exatamente um caminho pra acesso irrestrito, e não é um botão de UI. |
| **Admin** | Toda permissão que existe, igual ao Owner, mas *é* atribuível pela UI. |
| **Moderator** | `console.read`, `console.chat`, `console.commands`, `players.view`, `players.moderate` |
| **Operator** | `server.start`, `server.stop`, `console.read`, `console.chat`, `players.view` |
| **Viewer** | `console.read`, `performance.view`, `players.view` |

**O arquivo de seed de permissões** ([`services/seed.go`](../services/seed.go)) — um operador larga `permissions-seed.json` do lado de `server.properties` (ou na raiz do repo como fallback) *antes* do primeiro deploy, pra pré-atribuir role sem fixar username no código:

```json
[
  { "username": "lomokwa", "role": "Owner" },
  { "username": "Ant", "role": "Admin" }
]
```

`ApplyPermissionsSeed` roda em **todo** boot, não só no primeiro — um username que ainda não se registrou é tentado de novo no próximo restart, e uma atribuição já existente nunca é sobrescrita (então é seguro deixar o arquivo lá pra sempre, ou corrigir um typo nele e reiniciar).

### 5.3 Resolução de servidor (`:sid`)

`ResolveServer()` ([`middleware/server.go`](../middleware/server.go)) é montado no grupo `/api/servers/:sid`. Ele procura `:sid` no registro, devolve 404 se o id for desconhecido (antes de qualquer handler rodar), e guarda o `*services.ServerRuntime` correspondente no contexto do gin. Handlers leem de volta com [`runtimeFromRequest(c)`](../handlers/runtime.go) — todo handler que toca "o servidor" passa por aqui, tenha sido alcançado pela rota flat (que resolve pro runtime default) ou por uma namespaced.

## 6. Referência da API HTTP

Montada em `newRouter()` de [`main.go`](../main.go). Duas famílias cobrem os mesmos handlers:

- **Rotas flat** (`/api/players`, `/api/start`, …) — sempre resolvem pro servidor *default*. São **permanentes**, não descontinuadas: o [selton-mello-bot](https://github.com/lomokwa/selton-mello-bot) (um bot de Discord com deploy separado) chama `/api/players` sem nenhuma forma de aprender sobre rotas namespaced no próprio ritmo. Quebrar isso quebra produção. Ver `TestFlatRoutes_StillRouteAndMatchDefaultServer` em [`main_routes_test.go`](../main_routes_test.go).
- **Rotas namespaced** (`/api/servers/:sid/players`, …) — o *mesmo* handler, resolvido contra o servidor que `:sid` nomear.

Coluna Auth: 🔓 pública · 🔑 JWT (qualquer logado) · 🔑+`perm` JWT com a permissão nomeada · 🗝️ API key ou JWT.

| Método | Caminho flat | Caminho namespaced | Auth | Handler |
|---|---|---|---|---|
| POST | `/api/register` | — | 🔓 | [`RegisterHandler`](../handlers/users.go#L37) |
| POST | `/api/login` | — | 🔓 | [`LoginHandler`](../handlers/users.go#L78) |
| GET | `/api/invitations/:token` | — | 🔓 | [`ValidateInvitationHandler`](../handlers/users.go#L26) |
| POST | `/api/admin/invitations` | — | 🗝️ | [`CreateInvitationHandler`](../handlers/users.go#L13) |
| GET | `/api/me` | — | 🔑 | [`GetMeHandler`](../handlers/users.go#L54) |
| GET | `/api/users` | — | 🔑+`admin.manage_users` | [`GetUsersHandler`](../handlers/users.go#L68) |
| GET | `/api/permissions/schema` | — | 🔑 | [`PermissionSchemaHandler`](../handlers/roles.go#L22) |
| GET | `/api/me/permissions` | — | 🔑 | [`MyPermissionsHandler`](../handlers/roles.go#L29) |
| GET | `/api/roles` | — | 🔑+`admin.manage_roles` | [`ListRolesHandler`](../handlers/roles.go#L48) |
| GET | `/api/users/:id/permissions` | — | 🔑+`admin.manage_roles` | [`GetUserPermissionsHandler`](../handlers/roles.go#L59) |
| PUT | `/api/users/:id/role` | — | 🔑+`admin.manage_roles` | [`SetUserRoleHandler`](../handlers/roles.go#L92) |
| PUT | `/api/users/:id/overrides` | — | 🔑+`admin.manage_roles` | [`SetUserOverridesHandler`](../handlers/roles.go#L141) |
| GET | `/api/me/mclink` | — | 🔑 | [`GetMcLinkHandler`](../handlers/mclink.go#L178) |
| POST | `/api/me/mclink/start` | — | 🔑 | [`StartMcLinkHandler`](../handlers/mclink.go#L66) |
| POST | `/api/me/mclink/verify` | — | 🔑 | [`VerifyMcLinkHandler`](../handlers/mclink.go#L127) |
| DELETE | `/api/me/mclink` | — | 🔑 | [`UnlinkMcHandler`](../handlers/mclink.go#L205) |
| POST | `/api/server` | — | 🔑+`server.start` | [`CreateServerHandler`](../handlers/server.go#L24) |
| GET | `/api/server` | — | 🔑 | [`ServerExistsHandler`](../handlers/server.go#L149) |
| DELETE | `/api/server` | — | 🔑+`server.stop` | [`DeleteServerHandler`](../handlers/server.go#L119) |
| POST | `/api/start` | `/api/servers/:sid/start` | 🔑+`server.start` | [`StartServerHandler`](../handlers/server.go#L90) |
| POST | `/api/stop` | `/api/servers/:sid/stop` | 🔑+`server.stop` | [`StopServerHandler`](../handlers/server.go#L171) |
| GET | `/api/status` | `/api/servers/:sid/status` | 🔑 | [`StatusHandler`](../handlers/server.go#L192) |
| GET | `/api/console` (WS) | `/api/servers/:sid/console` | 🔑+`console.read` | [`ConsoleHandler`](../handlers/console.go#L71) — ver [§10](#10-o-protocolo-do-console-websocket) |
| GET | `/api/players` | `/api/servers/:sid/players` | 🔑+`players.view` | [`ListPlayersHandler`](../handlers/players.go#L11) |
| GET | `/api/properties` | `/api/servers/:sid/properties` | 🔑+`settings.view` | [`GetServerPropertiesHandler`](../handlers/properties.go#L8) |
| PATCH | `/api/properties` | `/api/servers/:sid/properties` | 🔑+`settings.edit` | [`UpdateServerPropertiesHandler`](../handlers/properties.go#L19) |
| GET | `/api/files` | `/api/servers/:sid/files` | 🔑+`files.read` | [`ListFilesHandler`](../handlers/files.go#L51) |
| GET | `/api/files/read` | `/api/servers/:sid/files/read` | 🔑+`files.read` | [`ReadFileHandler`](../handlers/files.go#L94) |
| PUT | `/api/files` | `/api/servers/:sid/files` | 🔑+`files.edit` | [`WriteFileHandler`](../handlers/files.go#L129) |
| GET | `/api/files/download` | `/api/servers/:sid/files/download` | 🔑+`files.read` | [`DownloadFileHandler`](../handlers/files.go#L165) |
| POST | `/api/files/upload` | `/api/servers/:sid/files/upload` | 🔑+`files.upload` | [`UploadFileHandler`](../handlers/files.go#L188) |
| DELETE | `/api/files` | `/api/servers/:sid/files` | 🔑+`files.delete` | [`DeleteFileHandler`](../handlers/files.go#L239) |
| GET | `/api/backups` | `/api/servers/:sid/backups` | 🔑+`backups.view` | [`ListBackupsHandler`](../handlers/backups.go#L21) |
| POST | `/api/backups` | `/api/servers/:sid/backups` | 🔑+`backups.create` | [`CreateBackupHandler`](../handlers/backups.go#L42) |
| DELETE | `/api/backups` | `/api/servers/:sid/backups` | 🔑+`backups.delete` | [`DeleteBackupHandler`](../handlers/backups.go#L66) |
| GET | `/api/backups/download` | `/api/servers/:sid/backups/download` | 🔑+`backups.download` | [`DownloadBackupHandler`](../handlers/backups.go#L94) |
| POST | `/api/backups/restore` | `/api/servers/:sid/backups/restore` | 🔑+`backups.restore` | [`RestoreBackupHandler`](../handlers/backups.go#L125) |
| GET | `/api/backups/config` | `/api/servers/:sid/backups/config` | 🔑+`backups.view` | [`GetBackupConfigHandler`](../handlers/backups.go#L165) |
| PUT | `/api/backups/config` | `/api/servers/:sid/backups/config` | 🔑+`backups.create` | [`UpdateBackupConfigHandler`](../handlers/backups.go#L187) |
| GET | `/api/servers` | — | 🔑 | [`ListServersHandler`](../handlers/servers.go#L61) |
| GET | `/api/servers/:sid` | — | 🔑 | [`GetServerHandler`](../handlers/servers.go#L98) |
| GET | `/api/docs/*any` | — | 🔓 | UI do Swagger, ver abaixo |

**Swagger.** Todo handler carrega anotações `@Summary`/`@Router` consumidas pelo [`swaggo/swag`](https://github.com/swaggo/swag) (`go generate` no topo de `main.go` regenera [`docs/docs.go`](docs.go)); navegue pela versão viva e sempre atualizada em `/api/docs/index.html` em qualquer deploy rodando, em vez de confiar que a descrição desta tabela vai ficar perfeitamente sincronizada.

**Envelope de resposta** — todo handler responde com [`types.APIResponse`](../types/response.go): `{"success": bool, "data"?: T, "error"?: string}`. O `apiFetch` do frontend (ver o guia do cliente) é escrito especificamente contra esse formato.

## 7. Módulos de domínio

### 7.1 Ciclo de vida do servidor & o registro multi-servidor

**Arquivos:** [`services/runtime.go`](../services/runtime.go) (o tipo `ServerRuntime` + carregamento do registro), [`services/servers.go`](../services/servers.go) (helpers tipo CRUD sobre a tabela `servers`), [`services/process.go`](../services/process.go) (start/stop/status via o plano de controle), [`services/minecraft.go`](../services/minecraft.go) (download do jar, `server.properties`, lista de jogadores).

`ServerRuntime` ([`services/runtime.go`](../services/runtime.go#L17)) guarda tudo que era global de pacote antes do registro multi-servidor existir: um `*types.LogHub`, um mutex de escrita no console, um mutex de backup, um cache de jogadores online — tudo com escopo de um diretório de servidor (`Dir`). Sempre usado como ponteiro. [`DefaultRuntime()`](../services/runtime.go#L130) e [`RuntimeForID(id)`](../services/runtime.go#L146) são as duas formas de conseguir um; todo handler passa por [`runtimeFromRequest`](../handlers/runtime.go) em vez de chamar essas direto, então rota flat e namespaced compartilham um caminho de código só.

A derivação de path ([`ControlDir`](../services/runtime.go#L53), `ConsoleFifoPath`, `LatestLogPath`, etc.) usa **concatenação de string de propósito, não `filepath.Join`** — pro runtime default, cujo `Dir` é exatamente a constante `ServerDir`, todo método precisa produzir um resultado idêntico byte a byte ao seu equivalente em `constants.go` (`filepath.Join` removeria silenciosamente o `./` inicial). Ver `TestDefaultRuntime_MatchesExistingConstants` em [`services/runtime_test.go`](../services/runtime_test.go).

`StartServerProcess`/`StopServerProcess` ([`services/process.go`](../services/process.go)) não sobem um processo direto — escrevem um verbo de ciclo de vida em `control.in` e ficam consultando `status.json` (via [`ReadStatus`](../services/process.go#L38)) até o supervisor reportar o novo estado. `IsServerRunning()` trata um heartbeat com mais de 10 segundos como "não rodando", mesmo se o último `running: true` gravado dissesse o contrário — um supervisor travado lê como parado, em vez de um "no ar" falso.

### 7.2 Streaming do console

**Arquivos:** [`services/logtail.go`](../services/logtail.go) (acompanha o `latest.log`), [`types/hub.go`](../types/hub.go) (`LogHub` — o fan-out pub/sub), [`handlers/console.go`](../handlers/console.go) (o endpoint WebSocket, ver [§10](#10-o-protocolo-do-console-websocket)).

Cada `ServerRuntime` ganha sua própria goroutine de tail (`tailLoopFor`), iniciada uma vez via `sync.Once` quando [`LoadRuntimes`](../services/runtime.go#L110) sobe. Detecta rotação de log de duas formas — identidade de arquivo via `os.SameFile`, ou o arquivo encolhendo abaixo do offset de leitura — qualquer uma das duas significa "reabrir do zero", já que a linha de prontidão do próprio Minecraft nunca pode ser perdida depois de um restart.

Ao (re)conectar, um novo assinante recebe o **backlog**: até 200 das linhas completas mais recentes do arquivo ([`readBacklog`](../services/logtail.go#L136), lendo no máximo uma janela de 256 KiB), então o console nunca fica em branco logo depois de um restart da API mesmo com a JVM rodando o tempo todo.

### 7.3 Jogadores

**Arquivo:** `ListPlayers` de [`services/minecraft.go`](../services/minecraft.go) ([linha 319](../services/minecraft.go#L319)).

Lê três arquivos estáticos dentro do diretório do servidor (`usercache.json`, `ops.json`, `banned-players.json`, `whitelist.json`) e cruza com um comando `list` de console ao vivo (`GetOnlinePlayers`, [linha 249](../services/minecraft.go#L249), cacheado brevemente por runtime pra não martelar o console a cada poll). Um arquivo opcional ausente (ops/banned/whitelist) degrada pra "ninguém nessa categoria" em vez de erro; `usercache.json` em si é obrigatório (nenhum jogador entrou ainda = nada pra listar).

### 7.4 Backups

**Arquivos:** [`services/backup.go`](../services/backup.go) (criar/restaurar/deletar/podar, todos métodos de `*ServerRuntime`), [`services/backup_scheduler.go`](../services/backup_scheduler.go) (a goroutine de backup agendado).

`CreateBackup` zipa o diretório do mundo depois de pedir pro servidor rodando fazer `save-off` / `save-all flush` / `save-on` (pro zip ficar consistente mesmo com jogador online); `RestoreBackup` recusa enquanto o servidor está rodando (ver a descrição de `PermBackupsRestore` — essa é a ação mais destrutiva do sistema inteiro) e descompacta com guarda contra path-traversal (zip-slip). `PruneBackups(keep)` apaga os mais antigos além de `keep`, chamado depois de todo backup agendado conforme `backup_config.keep`.

`NotifyBackupConfigChanged()` ([`services/backup_scheduler.go` linha 21](../services/backup_scheduler.go#L21)) é como `UpdateBackupConfigHandler` acorda a goroutine do agendador na hora depois de uma mudança de config, em vez de esperar até um intervalo inteiro pra ela perceber.

### 7.5 Gerenciador de arquivos

**Arquivo:** [`handlers/files.go`](../handlers/files.go).

Todo path passa por `safePath` (mesmo arquivo, não exportada) antes de tocar o disco: rejeita traversal com `..` e recusa qualquer path dentro do diretório de controle reservado `.mcmanager`, ambos sob o *próprio* runtime do servidor — ver `TestSafePath_TraversalDeniedUnderArbitraryRuntime` em [`handlers/files_test.go`](../handlers/files_test.go) pra entender por que "runtime arbitrário" é testado explicitamente, não só o default.

### 7.6 Vínculo de conta Minecraft

**Arquivo:** [`handlers/mclink.go`](../handlers/mclink.go) + helpers em [`services`](../services/).

Uma conta do site prova que é dona de um username do Minecraft recebendo um código de uso único **dentro do jogo** (via `tellraw`, pela conexão do console) e confirmando na UI web dentro de uma janela curta de expiração (tabela `mc_link_codes`). Uma tentativa pendente por usuário; começar uma nova sobrescreve o que estava pendente. Vínculos verificados ficam em `minecraft_links`.

### 7.7 Usuários, convites, login

**Arquivos:** [`handlers/users.go`](../handlers/users.go), [`services/users.go`](../services/users.go).

Registro é só por convite: `CreateInvitation` gera um token aleatório com expiração, `Register` consome ele (marca `used_at`, recusa o mesmo token duas vezes). Senha é hasheada com bcrypt (ver `Login`/`Register` em [`services/users.go`](../services/users.go)); `Login` gera o JWT descrito em [§5.1](#51-middleware-de-autenticação).

## 8. Modelo de dados

O schema mora inteiro em [`db/migrations.sql`](../db/migrations.sql), aplicado com um único `Exec` do arquivo inteiro em todo boot ([`db/db.go`](../db/db.go)). Toda instrução é `CREATE TABLE IF NOT EXISTS` — **não existem migrations numeradas/versionadas**; uma mudança de schema aqui é aditiva só, por convenção, porque isso roda contra um banco de produção ao vivo em todo deploy, sem etapa de migration separada.

| Tabela | Propósito | Colunas-chave |
|---|---|---|
| `users` | Contas do site | `username` único, `password_hash` (bcrypt) |
| `invitations` | Tokens de registro só-por-convite | `token` único, `expires_at`, `used_at` |
| `backup_config` | Agendamento de backup, linha única (`id=1`) | `enabled`, `interval_minutes`, `keep` |
| `roles` | Pacotes nomeados de permissão | `permissions` (array JSON), `is_system` |
| `user_roles` | Uma linha por usuário-com-role | `role_id`, `overrides` (mapa JSON) — **ausência = negado por padrão** |
| `minecraft_links` | Vínculos de conta verificados | `mc_username`, `mc_uuid` |
| `mc_link_codes` | Tentativas de vínculo pendentes | `code`, `expires_at` — uma por usuário |
| `servers` | O registro multi-servidor ([§7.1](#71-ciclo-de-vida-do-servidor--o-registro-multi-servidor)) | `id`, `dir` único, `port`, `xms`/`xmx` |

`roles.permissions` e `user_roles.overrides` guardam valores de `Permission` como JSON — ver [§5.2](#52-permissões--o-modelo-rbac) pra como eles se combinam.

## 9. Observabilidade

**Log estruturado** ([`logging.go`](../logging.go)): toda linha de log passa por `log/slog`. `GIN_MODE=debug` escolhe um handler de texto legível; qualquer outra coisa (ou seja, produção) escolhe JSON, que é o que a stack Promtail/Loki do homelab consome. `LOG_LEVEL` escolhe o nível mínimo, padrão `info`.

**O logger de acesso** (`requestLogger()` em [`main.go`](../main.go#L282)) é próprio, não o padrão do gin — o **status HTTP escolhe o nível do slog**: 5xx → `ERROR`, 4xx → `WARN`, senão `INFO`. Isso importa operacionalmente: uma onda de `403` (por exemplo, uma conta que perdeu a role depois de uma mudança de permissão) agora aparece como uma onda de linhas `WARN` em vez de se misturar no tráfego normal. Toda linha de log de acesso carrega `status`, `method`, `path`, `latency_ms`, `ip`.

**Redação de credencial.** Os query params `key` e `token` — os dois genuinamente viajam em URL (a API key de admin, e o JWT do WebSocket do console, já que browser não seta header no handshake de WS) — são removidos do path logado por `credentialParamRe` ([`main.go` linha 277](../main.go#L277)) antes da linha ser escrita. Travado por [`main_redaction_test.go`](../main_redaction_test.go).

**Healthcheck.** `./server healthcheck` (o mesmo binário, um branch especial no primeiro argumento, checado *antes* de `setupLogging` ou de qualquer parte da sequência de boot) faz um GET HTTP contra o próprio `/healthz` e sai com 0/1. É isso que o healthcheck do serviço `mc-manager` no `docker-compose.yml` roda — sem precisar de `curl`/`wget` na imagem de runtime. `/healthz` é deliberadamente sem autenticação (uma sonda não consegue carregar um JWT antes de alguém ter logado) e não responde nada além de `{"status":"ok"}`.

**pprof.** `net/http/pprof` é registrado num listener *separado*, vinculado só a `127.0.0.1:6060` (ver [`main.go` linha 33](../main.go#L33)) — nunca entra no router público do Gin, só alcançável via `docker exec` de dentro do container.

## 10. O protocolo do console (WebSocket)

Endpoint: `GET /api/console` (flat) ou `GET /api/servers/:sid/console` (namespaced), promovido de HTTP por [`ConsoleHandler`](../handlers/console.go#L71).

- **Auth:** JWT via `?token=` (ver [§5.1](#51-middleware-de-autenticação)) mais permissão `console.read`, checado antes do upgrade.
- **Guarda contra CSWSH:** o callback `Upgrader.CheckOrigin` permite `Origin` vazio (clientes não-browser — o bot de Discord, scripts), `Origin` do mesmo host, ou um listado em `CORS_ALLOWED_ORIGINS`. Qualquer outra coisa é rejeitada antes do handshake terminar.
- **Servidor → cliente:** toda linha completa que o tailer lê de `latest.log` é transmitida literalmente como frame de texto, mais a rajada de backlog na conexão (ver [§7.2](#72-streaming-do-console)).
- **Cliente → servidor:** um frame de texto é tratado como comando de console cru. [`classifyConsoleInput`](../handlers/console.go#L216) decide qual permissão ele precisa — uma linha começando com `say ` (sensível a maiúscula, batendo com o comando de verdade) precisa de `console.chat`; qualquer outra coisa precisa de `console.commands`. Um comando que quem chamou não tem permissão pra mandar nunca chega na JVM; o socket recebe um frame de erro JSON no lugar (`{"error": "..."}`).
- **Liveness:** um ciclo de ping/pong de 30s mais deadlines de leitura/escrita fecham conexões cujo par sumiu sem um FIN limpo de TCP (um NAT/proxy morto), pra goroutine + inscrição no hub não vazarem pra sempre.
- **Sinal de servidor parado:** um poll de status de 2s dentro do mesmo handler fecha o socket com um frame de fechamento normal assim que `IsServerRunning()` vira falso, já que o hub em si é de vida longa (sobrevive a qualquer execução única da JVM) e não fecha mais no stop como a versão anterior ao container único fazia.

## 11. Testes

`go test ./... -race -cover` é o que o CI roda (ver [§12](#12-cicd-e-deploy)) — rode sempre com `-race` localmente também se estiver mexendo em algo concorrente (o tailer de log, o handler do console, o agendador de backup, todos têm goroutine).

Convenções que vale saber antes de adicionar um teste:

- Todo pacote com testes define seus próprios helpers `setupTestDB(t)` / `setupServerDir(t)` (procure por eles — são duplicados por pacote de propósito, não importados de um lugar compartilhado, então a suíte de teste de cada pacote não depende de arquivo-não-teste de nenhum outro pacote).
- `main_routes_test.go` monta o `*gin.Engine` **de verdade** via `newRouter()` e dirige com `httptest` — é isso que prova que os pares de rota flat/namespaced se comportam de fato igual, não só que cada handler funciona isolado.
- 3 testes são conhecidos por falhar em Windows nativo e passar nos runners Linux do CI: `TestSafePath_ValidPathResolves` (separador de path literal), `TestRotated_DifferentFileDetected` e `TestRotated_MissingPathIsNotRotated` (os dois fazem `os.Remove` num arquivo aberto, que o Windows recusa). Não persiga esses localmente.
- `gofmt -l .` num checkout Windows com `core.autocrlf=true` acusa quase todo arquivo como falso positivo (CRLF, não formatação de verdade). Pra checar formatação de verdade no Windows, normalize as quebras de linha numa cópia temporária primeiro, e rode o `gofmt` lá.

## 12. CI/CD e deploy

**CI** ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) em todo push/PR pra `main`: `gofmt -l` (tem que vir vazio), `go vet`, `go build`, `go test -race -cover`, depois [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck). Qualquer um desses falhando é um X vermelho **pro repo inteiro** — inclusive em PRs cujo diff não tem nada a ver com a falha (o passo do `govulncheck` em particular pode ficar vermelho só porque uma CVE nova foi publicada contra o toolchain Go fixado, sem nenhuma mudança de código).

**Deploy** ([`.github/workflows/deploy.yml`](../.github/workflows/deploy.yml)) é um workflow *separado*, disparado por `workflow_run` **só depois que o CI reporta `success`** na `main`. Na prática: `git pull && docker compose up -d --build --no-deps mc-manager` via SSH (por `cloudflared access ssh`) no host do homelab. O `--no-deps` é o ponto todo — reconstrói só a imagem `mc-manager`, nunca toca no serviço `minecraft` nem na JVM dentro dele.

**Consequência que vale internalizar:** um CI vermelho na `main` significa que deploy silenciosamente para de acontecer — não tem alerta separado, o merge simplesmente não vai pro ar. Sempre confira que o CI está verde na `main` antes de assumir que um PR mergeado está em produção.

**O que nunca sobe sozinho.** Qualquer coisa que só faz efeito com o container `minecraft` *recriado* (mudança no `docker-compose.yml` desse serviço, mudança de código no `cmd/supervisor`, `Dockerfile.minecraft`) precisa de um `docker compose up -d --build minecraft` rodado na mão no host — que **desliga** a JVM e desconecta jogador. Planeje isso como uma etapa própria, deliberada, separada de um merge normal.

## 13. Frontend, em resumo

A interface web é um repositório separado, [`lomokwa/mc-manager-client`](https://github.com/lomokwa/mc-manager-client) (React 19 + TypeScript + Vite), que fala com esta API só por HTTP/JSON e pelo WebSocket do console descrito em [§10](#10-o-protocolo-do-console-websocket). Não tem nenhum outro acoplamento com este código — sem pacote de tipos compartilhado, sem cliente gerado; o formato `{success, data, error}` de `types/response.go` e as rotas do [§6](#6-referência-da-api-http) são o contrato inteiro. Veja o [`docs/GUIA_TECNICO.md`](https://github.com/lomokwa/mc-manager-client/blob/main/docs/GUIA_TECNICO.md) dele pra arquitetura do frontend, tabela de página/rota, e modelo de gerenciamento de estado.

## 14. Receituário — "eu quero…"

| Eu quero… | Comece aqui |
|---|---|
| Adicionar um endpoint REST novo | Escreva o handler em `handlers/`, registre em `newRouter()` ([`main.go`](../main.go)) — a forma flat *e* a namespaced se for por servidor, protegido com `perm(types.PermX)` |
| Adicionar uma permissão nova | Adicione a const `Permission` + uma entrada `PermissionInfo` na zona certa em [`types/permissions.go`](../types/permissions.go); decida quais `BuiltinRoles` devem ter ela por padrão |
| Mudar o que uma role pode fazer por padrão | [`types.BuiltinRoles`](../types/permissions.go#L158) — lembre que `EnsureBuiltinRoles` só *insere*, não atualiza uma linha existente, então uma mudança aqui precisa de um caminho de migration de verdade pra alcançar bancos já em produção, não só um edit de código |
| Pré-atribuir role num deploy novo | Larga um `permissions-seed.json` do lado de `server.properties` — ver [§5.2](#52-permissões--o-modelo-rbac) |
| Adicionar um campo no registro de servidores | Tabela `servers` em [`db/migrations.sql`](../db/migrations.sql) + [`types/server.go`](../types/server.go) + [`services/servers.go`](../services/servers.go) |
| Mudar como o console classifica um comando | [`classifyConsoleInput`](../handlers/console.go#L216) em `handlers/console.go` |
| Mudar o que entra num backup agendado | `CreateBackup` em [`services/backup.go`](../services/backup.go), e o loop do agendador em [`services/backup_scheduler.go`](../services/backup_scheduler.go) |
| Mudar o nível ou os campos de uma linha de log | Todo call site usa `log/slog` direto (`slog.Info(...)`, `slog.Error(...)`) — sem wrapper pra contornar |
| Entender por que uma requisição deu 403 | O corpo da resposta nomeia a permissão que falta; confira `EffectivePermissions` em [`services/permissions.go`](../services/permissions.go) pra esse usuário |
| Entender por que um deploy não subiu | Confira o CI na `main` primeiro (ver [§12](#12-cicd-e-deploy)) — deploy só roda depois do CI passar |
| Adicionar uma env var | Leia com `os.Getenv` onde precisar, depois documente no [§4](#4-configuração-variáveis-de-ambiente) deste arquivo |
| Rodar tudo localmente | `docker compose up --build` pro modelo de dois containers, ou `go run .` contra um servidor Minecraft subido de outro jeito (a API degrada normalmente se os arquivos do plano de controle ainda não existirem — ver `services/process.go`) |

## 15. Glossário

- **Runtime** — um `*services.ServerRuntime`: o diretório, hub de log e mutexes de um servidor. Não é runtime do Go, nem runtime de container.
- **Registro** (registry) — a tabela SQL `servers` + o `map[string]*ServerRuntime` em memória montado a partir dela no boot.
- **Supervisor** — `cmd/supervisor`, o *outro* binário deste repo, rodando dentro do container `minecraft`.
- **Plano de controle** — os três arquivos (`console.in`, `control.in`, `status.json`) que a API e o supervisor usam pra conversar sem canal de rede.
- **Rota flat** — uma rota sem `/servers/:sid/`, sempre resolvendo pro servidor default, mantida pra sempre por compatibilidade (selton-mello-bot).
- **Permissões efetivas** — o padrão de uma role com os overrides de um usuário aplicados por cima; ver [§5.2](#52-permissões--o-modelo-rbac).
- **Backlog** — a rajada de linhas de log recentes que um cliente de console recebe assim que conecta, vinda de [`readBacklog`](../services/logtail.go#L136).

# Choosing which tools load

Every tool an assistant is given takes room it could use to think. So sonarr-mcp loads a few tools unless you ask for more.

## Toolsets

**The default is `core`:** six read-only tools, about 1,000 tokens. Every tool at once is around 22,100 tokens before anyone asks a question.

`--toolsets`, or `SONARR_TOOLSETS`, loads the groups a session needs. `core` comes along with whatever else is asked for, because nothing else can find a series.

| toolset | what it is for | tools | ~tokens |
|---|---|---|---|
| `core` | Find and read series, episodes and the queue. | 6 | 1,000 |
| `library` | Add shows and root folders. | 11 | 1,800 |
| `curation` | Every audit, and everything that fixes what they find. | 54 (55) | 9,500 (9,600) |
| `admin` | Health, logs, backups, restarting, and deleting series and files. | 26 (29) | 3,500 (4,050) |
| `settings` | Set Sonarr up: its settings, indexers, download clients, notifications, import lists, profiles, formats and rules. | 54 (65) | 10,400 (11,900) |
| `all` | Everything. | 127 (142) | 22,100 (24,300) |

Counts include `core`. The figure in brackets is with `--enable-delete`.

For day-to-day care of a library, use `curation`.

## What a token is here

Tokens are what the assistant is sent for each tool: its name, description and arguments. They are measured from a real tool list, at four bytes a token.

Each tool also describes the shape of its answer, another 26,200 tokens across `all`. Clients keep that to check answers with, and do not send it to the assistant.

## By the start of a name

`--toolsets` also takes the first part of a tool's name, such as `series`, `episode`, `queue`, `history` or `audit`. That loads every tool starting with it. `sonarr-mcp tools` lists them all.

```sh
SONARR_TOOLSETS=all                 # every tool
SONARR_TOOLSETS=curation            # the audits, and everything that fixes what they find
SONARR_TOOLSETS=audit               # the audits alone, nothing that changes Sonarr
SONARR_TOOLSETS=core,queue,history  # core, and every queue_ and history_ tool
```

## Seeing what would load

`sonarr-mcp tools` prints what the current flags would load, by toolset. It needs no server.

```sh
sonarr-mcp tools                    # what loads by default
sonarr-mcp tools --toolsets all     # every tool
sonarr-mcp tools --read-only -q     # names only
```

## Narrowing further

`--allow-tools` and `--deny-tools` take tool names, separated by commas. A name may have a `*` at either end. `essential` stands for five tools: `series_list`, `series_get`, `calendar_list`, `queue_list` and `series_search`.

- **An allow list on its own** loads only the tools it names.
- **Beside `--toolsets`,** an allow list adds its tools to the sets.
- **A deny list** takes tools out of whatever else was asked for. That is how a set is narrowed.
- **A pattern that matches no tool** stops the server starting, and is named. A typo cannot quietly hide a tool.
- **Neither list** gets a tool past `--read-only`, or past the delete switch.

```sh
SONARR_ALLOW_TOOLS=essential                                     # exactly those five
SONARR_ALLOW_TOOLS=series_*,episode_list,queue_list              # every series_ tool, and two more
SONARR_ALLOW_TOOLS=audit_*                                       # the audits alone
SONARR_TOOLSETS=curation SONARR_ALLOW_TOOLS=task_list,task_run   # curation, and two tools from admin
SONARR_TOOLSETS=curation SONARR_DENY_TOOLS=*_search              # curation, without the tools that search
SONARR_DENY_TOOLS=*_delete,*_search
```

# gochat 部署手册（Docker 本地演示环境）

> 适用源码：本仓库当前快照（LockGit/gochat 源码，`go.mod` 声明 `go 1.18`）。
>
> 文档状态：**已完成源码静态核对，尚未做实际部署验证**。编写此文档的机器上 `docker` 命令不存在，也没有 `gcc`，因此下文所有 `docker pull`、`docker build`、`docker run`、健康检查和冒烟命令**均未执行**。文中“预期”是执行时的判据，不是实测结果；不要把“预期”改写成“已通过”。

## 1. 两条部署路径，以及它们真正的区别

| 路径 | 工具链来源 | 业务二进制 | 适用场景 |
| --- | --- | --- | --- |
| A：官方镜像 `lockgit/gochat:1.18` | Docker Hub 上作者推送的镜像 | 容器内现场编译 | 想最快看到一个能登录的演示站点 |
| B：本地构建镜像 `gochat-local:1.18` | 用本仓库 `docker/Dockerfile` 构建 | 容器内现场编译 | 官方镜像拉不到、架构不匹配，或需要自己控制基础镜像 |

关键事实（已按源码核对）：

- `docker/Dockerfile` **不编译**业务程序，它只做四件事：以 `golang:1.18` 为基础、装 `gcc automake autoconf libtool make supervisor`、下载 etcd v3.4.3 与 Redis 5.0.9 源码并编译安装、设置 `ENV RUN_MODE dev`。
- 业务程序由 `reload.sh` 在容器内用 `go build -o /go/src/gochat/bin/gochat.bin /go/src/gochat/main.go` 编译；源码目录通过 `-v` 挂载进容器。
- 所以 A、B 两条路径的差别只在“工具链（Go、etcd、Redis、supervisor）来自官方镜像还是自己构建”，**业务代码都要在容器里重新编译**。不存在“镜像里已经有一个编译好的 gochat 可以直接跑”这回事。
- `Makefile` 的 `build` 目标执行的是 `cd ./docker && docker build -t lockgit/gochat:${TAG} .`；构建上下文是 `docker/` 子目录，`Dockerfile` 里的 `COPY . .` 只复制 `docker/` 目录里的内容，不会把整个源码打进镜像。

`readme.md` 里编译示例带 `-tags=etcd`，但当前源码中没有任何 `//go:build` / `// +build` 约束文件，`reload.sh` 也不带 tags。按 `reload.sh` 的方式编译即可，不需要额外 tags。

## 2. 版本、端口与运行结构

### 2.1 固定版本

| 组件 | 版本 | 来源 |
| --- | --- | --- |
| 官方镜像 | `lockgit/gochat:1.18` | `run.sh` 的 `DOCKER_IMAGE` |
| 基础镜像 | `golang:1.18` | `docker/Dockerfile` 第 1 行 |
| Go module | `go 1.18` | `go.mod` |
| etcd | v3.4.3（linux-amd64 二进制） | `docker/Dockerfile` |
| Redis | 5.0.9（源码编译） | `docker/Dockerfile` |
| 数据库 | SQLite 文件 `db/gochat.sqlite3` | `db/db.go` |

不要因为拉取失败就把标签改成 `latest`。`1.18` 只是标签，内容可能被上游重新推送；需要严格复现时，首次拉取后记录镜像 ID 与 RepoDigest，之后用 `lockgit/gochat@sha256:<digest>` 固定。

### 2.2 端口

| 端口 | 用途 | 是否发布到宿主机 |
| --- | --- | --- |
| 8080 | site 静态站点 | 是，建议只发布到 `127.0.0.1` |
| 7070 | api HTTP 接口 | 是，建议只发布到 `127.0.0.1` |
| 7000 | connect websocket | 是，建议只发布到 `127.0.0.1` |
| 7001、7002 | connect tcp | 是，建议只发布到 `127.0.0.1` |
| 6900、6901 | logic RPC | 否，容器内 |
| 6912、6913 | connect websocket RPC | 否，容器内 |
| 6914、6915 | connect tcp RPC | 否，容器内 |
| 6379 | Redis | 否，容器内 |
| 2379、2380 | etcd 客户端 / peer | 否，容器内 |

容器内监听地址由配置和代码决定，不要改：`config/dev/site.toml` 为 8080、`config/dev/api.toml` 为 7070，`api/chat.go` 与 `site/site.go` 都以 `":<port>"` 形式监听所有网卡；`config/dev/connect.toml` 里 websocket 是 `0.0.0.0:7000`、tcp 是 `0.0.0.0:7001,0.0.0.0:7002`。这些是**容器内**地址，Docker 端口转发依赖它们。宿主机侧则用 `-p 127.0.0.1:<端口>:<端口>` 收口。

上游 `run.sh` 用的是 `-p 8080:8080` 这种写法，等价于绑定宿主机所有网卡。本项目没有 TLS，`api/router/router.go` 的 CORS 还是 `*`，所以本手册统一改成只绑回环地址；这是本手册的部署口径，不是上游脚本的默认行为。

### 2.3 一个容器、supervisord 管八个进程

`run.sh` 的做法是：起一个容器，容器命令 `supervisord -n`，把 `docker/dev/supervisord.conf`、`docker/dev/supervisord.d`、`docker/dev/redis.conf` 分别挂到 `/etc/supervisord.conf`、`/etc/supervisord.d`、`/root/redis.conf`；`supervisord.conf` 里 `[include] files = supervisord.d/*.ini` 拉起八个程序：

| 程序 | 命令 | priority |
| --- | --- | --- |
| `etcd` | `/bin/etcd` | 1 |
| `redis` | `/bin/redis-server /root/redis.conf` | 2 |
| `gochat_logic` | `gochat.bin -module logic` | 3 |
| `gochat_connect_websocket` | `gochat.bin -module connect_websocket` | 4 |
| `gochat_connect_tcp` | `gochat.bin -module connect_tcp` | 5 |
| `gochat_task` | `gochat.bin -module task` | 6 |
| `gochat_api` | `gochat.bin -module api` | 7 |
| `gochat_site` | `gochat.bin -module site` | 8 |

所有程序都是 `autorestart = true`，没有设置 `startsecs`、`startretries`，用 supervisord 默认值（1 秒、3 次）。

**priority 只决定发起启动的先后，不等待前一个进程就绪。** etcd / Redis 还没能接受连接时，logic、connect、task 可能因为连不上而退出，supervisord 会按 `autorestart` 重试，重试耗尽后停在 `FATAL`。所以：

- 容器 `Up` 不等于部署成功；
- 首次启动时 `bin/gochat.bin` 还不存在，六个业务模块必然起不来，这是预期现象；
- `reload.sh` 最后的 `supervisorctl restart all` 是在基础设施已经跑起来之后的重启，是让六个模块收敛到正常状态的关键一步。

### 2.4 路径依赖：不能随便改挂载点

- `config/config.go` 用 `runtime.Caller` 取**编译期**的 `config` 包目录，再拼 `RUN_MODE`（默认 `dev`）读取 `config/dev/*.toml`。二进制在 `/go/src/gochat` 下编译，配置目录就是 `/go/src/gochat/config/dev`。
- `db/db.go` 用 `filepath.Abs("./")` 加 `db/gochat.sqlite3`，依赖进程工作目录。
- `site/site.go` 从 `./site` 提供静态文件，也依赖工作目录。
- supervisord 的 program 段没有配置 `directory`，子进程继承 supervisord 的工作目录；镜像 `WORKDIR /go/src/gochat`，容器命令 `supervisord -n` 也在该目录启动。

结论：源码必须挂到 `/go/src/gochat`，并在该目录下启动 supervisord；不要把 `bin/gochat.bin` 复制到别处单独运行。

## 3. 前置条件

- Windows 10/11 x64，Docker Desktop 使用 WSL2 后端，`docker` 在 PATH 中，且能连上 Linux 引擎。
- 至少 5 GB 空闲磁盘（镜像、构建缓存、Go 编译缓存）。
- 能访问 Docker Hub、GitHub Releases、`download.redis.io`（路径 B 构建时要下载 etcd 与 Redis）。
- `run.sh`、`reload.sh` 必须是 **LF** 换行，且保留可执行位（见第 11 节）。若被转成 CRLF，容器内执行会报 `$'\r': not found`；即使不报错，也会因为解释器路径末尾多一个 `\r` 而失败。

检查：

```powershell
$repo = (Resolve-Path .\gochat-master\gochat-master).Path
docker version
docker info --format '{{.OSType}}/{{.Architecture}}'
go version   # 仅确认本机是否有 Go；本手册的编译发生在容器内
```

预期：`docker version` 同时输出 Client 与 Server，Server 为 Linux；`docker info` 输出 `linux/amd64`。官方镜像里的 etcd 是 linux-amd64 二进制，ARM64 主机不要直接照搬两条路径，需先确认镜像与 etcd 的架构支持。

本机实测（写这份文档时）：`docker` 不存在；`go version` 为 `go1.25.3 windows/amd64`，但没有 `gcc`。所以既不能做容器验证，也不能拿宿主机原生编译的结果冒充容器结果（本项目 SQLite 驱动依赖 CGO）。

## 4. 凭据：只放在不会被提交的本地文件

本快照的默认配置里 Redis **没有密码**：`docker/dev/redis.conf` 没有 `requirepass`，`config/dev/common.toml` 与 `config/dev/task.toml` 的 `redisPassword` 都是空串，且 `redis.conf` 是 `bind 127.0.0.1` + `protected-mode yes`，只在容器内回环监听。

如果要给 Redis 加密码，遵守以下边界：

- 密码写在仓库根目录的 `.env.local` 里（`.gitignore` 已忽略，见第 11 节），**不要**写进 `docker/*/redis.conf`、`config/*/*.toml`、`Dockerfile` 或 compose 文件。
- 真正生效需要改两份**临时副本**：给临时副本的 `docker/dev/redis.conf` 追加 `requirepass <密码>`，并把 `config/dev/common.toml` 与 `config/dev/task.toml` 的 `redisPassword` 改成同一个值。本项目没有读取 `.env` 的逻辑（源码里没有 dotenv 依赖），`docker run --env-file` 只是把变量带进容器供 `redis-cli` 使用，不会自动改配置。
- 临时副本放在仓库外的 `tmp-deploy/` 目录（见第 5 节），冒烟结束后删除整个目录；不要把带密码的副本留在仓库里。
- 不要给 etcd 开认证：`supervisord.d/etcd.ini` 直接执行 `/bin/etcd`，没有认证参数，rpcx 注册端也不会传 etcd 账号密码，开了认证会导致注册失败。etcd 只放容器内、不发布 2379。

本手册后续示例按“Redis 无密码”的快照默认状态编写；加密码时把 `redis-cli PING` 改成带 `-a` 的形式，其余一致。

## 5. 建议在临时副本里部署，而不是直接挂仓库

`reload.sh` 会改动工作树：写 `bin/gochat.bin`、用 `sed` 改 `site/static/js/main.06044d49.js` 里的 API/WS 地址；运行中的 SQLite 还会改 `db/gochat.sqlite3`。直接挂仓库跑，这些都会被写进你的源码副本。

先复制一份到仓库外的临时目录（同一磁盘、路径尽量短）：

```powershell
$repo = (Resolve-Path .\gochat-master\gochat-master).Path
$work = Join-Path (Split-Path $repo -Parent) 'tmp-deploy\gochat'
if (Test-Path -LiteralPath $work) { throw "目标已存在，请先确认并手动清理: $work" }
New-Item -ItemType Directory -Path (Split-Path $work -Parent) -Force | Out-Null
robocopy $repo $work /E /XD .git /XF .env.local | Out-Null
if ($LASTEXITCODE -ge 8) { throw "robocopy 失败，退出码 $LASTEXITCODE" }
```

`robocopy` 退出码 0-7 都算成功（1 表示复制了文件），≥8 才是失败。复制后确认三件事存在：`$work\config\dev\common.toml`、`$work\db\gochat.sqlite3`、`$work\bin`（`go build -o bin/gochat.bin` 不会自动创建父目录，快照里已有 `bin/readme.md`）。

后面所有 `-v` 都指向 `$work`。

## 6. 路径 A：官方镜像

### 6.1 拉取并记录摘要

```powershell
docker pull lockgit/gochat:1.18
docker image inspect lockgit/gochat:1.18 --format '{{.Id}}|{{index .RepoDigests 0}}|{{.Os}}/{{.Architecture}}'
```

预期：拉取成功，`linux/amd64`。把镜像 ID、RepoDigest、拉取时间记下来（可写到单独的部署记录里，不要回填本手册充当已验证结论）。

失败处理：

- `no matching manifest for linux/arm64`：主机架构不匹配，不要用 `--platform` 硬伪装成 amd64 后当成功；先确认架构，必要时走路径 B 并在镜像层面明确平台。
- `pull access denied` / `manifest unknown`：标签不存在或网络不可达，不要改成 `latest`，直接走路径 B。
- 超时：先查 Docker Desktop 的代理/DNS 和镜像加速；仍失败走路径 B。

### 6.2 启动容器

```powershell
$image = 'lockgit/gochat:1.18'

docker run -d --name gochat-demo `
  -e TZ=Asia/Shanghai `
  -e RUN_MODE=dev `
  -e HOST_IP=127.0.0.1 `
  -v "${work}:/go/src/gochat" `
  -v "${work}\docker\dev\supervisord.d:/etc/supervisord.d" `
  -v "${work}\docker\dev\supervisord.conf:/etc/supervisord.conf" `
  -v "${work}\docker\dev\redis.conf:/root/redis.conf" `
  -p 127.0.0.1:8080:8080 `
  -p 127.0.0.1:7070:7070 `
  -p 127.0.0.1:7000:7000 `
  -p 127.0.0.1:7001:7001 `
  -p 127.0.0.1:7002:7002 `
  $image supervisord -n
```

说明：

- 三个 `-v` 的挂载点和 `run.sh` 一致，`redis.conf` 必须挂到 `/root/redis.conf`，因为 `redis.ini` 里写死了这个路径。
- `HOST_IP` 会被 `reload.sh` 用来改写前端 JS 里的地址；只填 `127.0.0.1`。填公网 IP 会让浏览器去请求那个地址。
- `RUN_MODE` 决定读 `config/dev` 还是 `config/prod`；两个目录的 `common.toml` 目前内容一致（Redis 地址都是 `127.0.0.1:6379`、密码为空），但不要混用。
- 不要写成 `-p 8080:8080`，那会把演示站点暴露到局域网。
- 容器删除后 etcd 数据、Redis 数据都会丢（没有挂持久卷），学习环境可接受。
- 如果按第 4 节给 Redis 加了密码，在 `docker run` 里追加 `--env-file .env.local`，让 `redis-cli -a "$GOCHAT_REDIS_PASSWORD"` 能在容器内取到密码；这不会自动修改 `common.toml` / `task.toml`，配置仍要按第 4 节在临时副本里渲染。

### 6.3 编译并重启

先确认 supervisord 已经起来（`run.sh` 里 `docker run -d` 之后立刻 `docker exec` 执行 `reload.sh`，存在 socket 还没创建的竞态，所以手工执行时先等一下）：

```powershell
$deadline = (Get-Date).AddSeconds(30)
while ((Get-Date) -lt $deadline) {
    docker exec gochat-demo supervisorctl pid 2>$null
    if ($LASTEXITCODE -eq 0) { break }
    Start-Sleep -Seconds 1
}
```

编译 + 重启：

```powershell
docker exec gochat-demo sh -c 'cd /go/src/gochat && sh ./reload.sh'
```

预期输出依次包含：

```text
replace api and websocket ip address...
build gochat.bin ...
restart all ...
all Done.
Beautiful ! Now, You can visit http://127.0.0.1:8080 , start the world.
```

`reload.sh` 做了三件事：把 `site/static/js/main.06044d49.js` 里的 API 地址改成 `${HOST_IP}:7070`、WS 地址改成 `${HOST_IP}:7000/ws`；用 `CGO_CFLAGS="-g -O2 -Wno-return-local-addr" go build` 编译到 `/go/src/gochat/bin/gochat.bin`；执行 `supervisorctl restart all`。

如果 `go build` 报 `gcc: not found`，说明当前镜像缺 gcc 或 PATH 不对。**不要**用 `CGO_ENABLED=0` 绕过：SQLite 驱动会变成 stub，登录、注册都会失败。改走路径 B 重新构建工具镜像。

## 7. 路径 B：本地构建工具镜像

### 7.1 构建

```powershell
docker build --platform linux/amd64 -t gochat-local:1.18 "$work\docker"
docker image inspect gochat-local:1.18 --format '{{.Id}}|{{.Os}}/{{.Architecture}}'
```

构建过程（与 `docker/Dockerfile` 一致）：`golang:1.18` → `apt-get install gcc automake autoconf libtool make supervisor` → 下载 `etcd-v3.4.3-linux-amd64.tar.gz` 并把 `etcd`、`etcdctl` 放到 `/bin` → 下载 `redis-5.0.9.tar.gz` 并 `make` 后把 `redis-server`、`redis-cli` 放到 `/bin` → `ENV RUN_MODE dev`。

注意：

- `golang:1.18` 也是可变的标签；要严格复现就先 `docker pull golang:1.18` 并记录 digest。
- 构建需要访问 GitHub 和 `download.redis.io`；网络受限会失败，按第 9 节回退，不要改版本号碰运气。
- `--platform linux/amd64` 是为了和 Dockerfile 里的 amd64 etcd 二进制一致；在 ARM 主机上会走模拟，性能很差，属于“能跑起来验证”而不是可用部署。

### 7.2 运行

把 6.2 的 `$image` 换成 `gochat-local:1.18`，其余命令不变。路径 B 同样是“容器内源码编译”，不是把二进制塞进镜像。

## 8. 健康检查

本项目**没有 `/health` 或 `/ready` 端点**（`api/router/router.go` 只注册了 `/user/*` 与 `/push/*`）。因此健康检查必须组合下面几项，任何单独一项都不足以判定“部署成功”。

### 8.1 Redis

```powershell
docker exec gochat-demo sh -c 'redis-cli PING'
```

预期 `PONG`。加了密码则用 `redis-cli -a "$GOCHAT_REDIS_PASSWORD" PING`。若返回 `Could not connect`，看 `docker exec gochat-demo cat /tmp/redis.err` 和 `supervisorctl status redis`。

### 8.2 etcd 与服务注册

```powershell
docker exec -e ETCDCTL_API=3 gochat-demo etcdctl endpoint health
docker exec -e ETCDCTL_API=3 gochat-demo etcdctl get --prefix /gochat_srv --keys-only
```

预期第一条输出 `127.0.0.1:2379 is healthy: successfully committed proposal: ...`；第二条至少能看到 `LogicRpc` 与 `ConnectRpc` 两组键（前缀来自 `config/dev/common.toml` 的 `basePath = "/gochat_srv"`）。没有键说明 logic / connect 没注册成功，先看 `.err` 日志，不要继续冒烟。

### 8.3 进程状态

```powershell
docker exec gochat-demo supervisorctl status
```

预期八行全部 `RUNNING`（etcd、redis、logic、connect_websocket、connect_tcp、task、api、site）。

判读：

- `RUNNING` 可能只是刚被 `autorestart` 拉起来。要看 `uptime` 是否持续增长，以及日志里是否反复 `exited` / `entered FATAL` / `BACKOFF`。
- 某行 `FATAL` 表示启动重试耗尽，先看对应 `.err`，不要用 `restart all` 反复刷掉证据。

### 8.4 业务探活

1）站点首页：

```powershell
(Invoke-WebRequest -UseBasicParsing 'http://127.0.0.1:8080/').StatusCode
```

预期 `200`。

2）API 登录（走 api → etcd → logic → Redis → SQLite，比端口探活强得多）：

```powershell
$body = '{"userName":"demo","passWord":"111111"}'
$login = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:7070/user/login' -ContentType 'application/json' -Body $body
$login | ConvertTo-Json -Depth 5
```

预期 `code` 为 `0`、`message` 为 `login success`、`data` 为非空 token（`tools/response.go` 的返回结构是 `{code,message,data}`，登录成功的 message 由 `api/handler/user.go` 传入）。演示账号 `demo / 111111` 来自上游 `readme.md`；仓库内的 `db/gochat.sqlite3` 是上游演示库，是否包含该账号、密码是否仍为 `111111`，本机未实际登录验证。

3）端口只监听回环：

```powershell
docker port gochat-demo
Get-NetTCPConnection -State Listen -LocalPort 8080,7070,7000,7001,7002 |
  Select-Object LocalAddress,LocalPort
```

预期 `docker port` 五行都以 `127.0.0.1` 开头，`Get-NetTCPConnection` 的 `LocalAddress` 为 `127.0.0.1`。出现 `0.0.0.0` / `::` 说明发布口径不对，停下来检查 `docker run` 参数。

### 8.5 日志证据

```powershell
docker logs --tail 100 gochat-demo
docker exec gochat-demo sh -c "grep -E 'spawned|exited|FATAL|BACKOFF' /tmp/supervisord.log | tail -n 40"
docker exec gochat-demo sh -c 'tail -n 50 /tmp/gochat_logic.err'
```

启动阶段的 `spawned` 正常；稳定后仍反复 `exited` / `FATAL` / `BACKOFF`，说明有进程在崩，不能算通过。

### 8.6 判定口径

| 现象 | 结论 |
| --- | --- |
| 仅容器 `Up` | 不足以下结论 |
| Redis `PONG` + etcd healthy + 八进程 RUNNING 且 uptime 增长 | 基础设施与进程层通过 |
| 上面再加站点 200、登录 `code=0` 且 token 非空 | 核心链路可用（仍是学习环境口径） |
| 任一模块 `FATAL`、日志反复重启、登录失败 | 不通过，按第 9 节回退 |

## 9. 回退

### 9.1 决策表

| 失败点 | 判据 | 回退动作 |
| --- | --- | --- |
| 官方镜像拉取失败 | 超时、`manifest unknown`、架构不匹配 | 转路径 B；不要改 `latest` |
| 基础镜像拉取失败 | `golang:1.18` 拉不下来 | 查 Docker Desktop 代理 / 镜像加速；仍失败则停止并记录 |
| 构建下载 etcd / Redis 失败 | `wget` 超时、404、证书错误 | 停止构建，保留日志；不要改版本号或跳过依赖 |
| 容器起不来 | `docker ps` 无容器、`docker logs` 报挂载/配置错误 | 查 Windows 路径共享与挂载文件名，修临时副本后重建 |
| Redis / etcd 不健康 | `PING` 非 `PONG`、`endpoint health` 失败 | 不进入业务冒烟；看 `/tmp/redis.err`、`/tmp/etcd.err`，必要时重建容器 |
| 业务模块 `FATAL` / 反复重启 | `supervisorctl status` 非 RUNNING 或日志反复 exited | 依次查 etcd 注册、Redis 密码、SQLite 文件权限与工作目录 |
| 登录失败 | `code != 0` 或 token 为空 | 查 etcd 键、logic 日志、SQLite 文件；不要用“首页能打开”代替 |
| 工作树被污染 | `git status` 出现 `bin/gochat.bin`、`db/gochat.sqlite3`、`site/static/js` 改动 | 删临时副本重来；不要 `git checkout` 覆盖用户自己的改动 |

### 9.2 官方镜像不可用时

保留拉取报错原文，改走路径 B（第 7 节），用 `gochat-local:1.18` 重新执行 6.2 的 `docker run`，其余不变，并在部署记录里注明“官方镜像不可用，实际走本地构建”。不要把路径 A 写成通过。

### 9.3 路径 B 也失败时

1. 先定位失败步骤：基础镜像、apt、etcd 下载、Redis 下载、`go build`。
2. 保存完整构建日志与报错原文。
3. 网络问题先修代理 / DNS / 镜像加速；仍不可用就停，不换 `latest` 试。
4. `go build` 失败时不要改业务代码绕过，本手册不包含代码修改。
5. 结论写“未验证”，不要用宿主机编译或运行的结果替代容器结果。

### 9.4 容器异常后的清理

```powershell
docker logs --tail 200 gochat-demo
docker exec gochat-demo supervisorctl status
docker rm -f gochat-demo
```

然后删除 `tmp-deploy` 临时目录，重新从快照复制。

### 9.5 回到“未部署”状态

- `docker rm -f gochat-demo`
- 删除 `tmp-deploy`
- 保留或删除 `.env.local`（删除后密码失效，需要重新生成）
- 镜像可以留着复用；怀疑镜像损坏时 `docker image rm` 后重新拉取 / 构建
- 确认仓库 `git status` 只剩文档、`.gitattributes`、`.gitignore` 这类预期改动

## 10. SQLite 并发限制（部署前必须知道）

这部分是当前实现的硬限制，不是配置问题：

1. **六个进程都打开同一个 SQLite 文件。** `db/db.go` 在包 `init()` 里打开 `./db/gochat.sqlite3`；`main.go` 编出的单个二进制包含全部模块，进程一启动就会执行这个 `init`，所以 logic、connect_websocket、connect_tcp、task、api、site 六个进程各有一份到同一文件的连接池，即使只有 logic 真正读写用户表。
2. **连接池参数合计不小。** `SetMaxIdleConns(4)`、`SetMaxOpenConns(20)`、`SetConnMaxLifetime(8s)`，六个进程理论上有大量并发连接去抢同一个文件的锁。
3. **单写者模型。** SQLite 同一时刻只允许一个写事务；当前代码没有显式开启 WAL，也没有设置自定义 busy timeout。驱动默认 `busy_timeout` 是 5000ms（见 `vendor/github.com/mattn/go-sqlite3/sqlite3.go` 中 `busyTimeout := 5000`），写锁超过这个时间就会返回 `database is locked`。
4. **Windows + bind mount 更差。** Docker Desktop 把 Windows 目录挂进 Linux 容器要过文件共享层，锁语义与 IO 性能都比容器内本地卷差；把 `db/` 挂在 Windows 目录上跑并发写入，不建议。
5. **不能水平扩容 logic。** 多个 logic 实例共享同一个 SQLite 文件会放大锁冲突；需要扩容就先换 MySQL 等数据库（`db/db.go` 注释也说明了这一点），那属于业务改造，不在本手册范围。
6. **冒烟前先备份** `db/gochat.sqlite3`，并且只在临时副本里写测试数据，不要把测试用户灌进源码快照。

因此本手册的定位是“单机学习环境演示”：单容器、单 logic、低并发。要当作多人真实服务，需要先解决数据库与消息可靠性，这两项当前实现都没有。

## 11. 换行、脚本权限、忽略规则与本机配置

### 11.1 换行与二进制

仓库根的 `.gitattributes` 做了两件事：

- 文本文件统一 `text=auto eol=lf`，其中 `*.sh`、`*.bash` 显式 `text eol=lf`，保证 `run.sh`、`reload.sh` 在 Windows 检出后仍是 LF。
- 位图与数据库文件（`*.png`、`*.jpg`、`*.gif`、`*.ico`、`*.sqlite3` 及其旁路文件、`*.bak`）标记为 `gcbinary`（即 `-text -diff -eol`），禁止换行转换和文本 diff；`*.svg` 本质是文本，保持 LF。

注意：`architecture/single_send.png.bak` 实际是 PNG 文件（文件头 `89 50 4E 47`），所以 `*.bak` 按二进制处理。启用 `.gitattributes` 后，若历史上已有文件以 CRLF 入库，需要单独执行一次 `git add --renormalize .` 并作为一个独立提交评估；本次任务**没有**做全仓库重规范化。

### 11.2 脚本可执行位

`run.sh`、`reload.sh` 在上游仓库里是 `100755`（本工作副本没有 `.git` 目录，无法用 `git ls-files -s` 就地复核）。Windows 检出通常带不出可执行位，所以：

- 容器内用 `sh ./reload.sh` 调用（`run.sh` 自己也是 `/bin/sh './reload.sh'`），不依赖可执行位；
- 在 Linux / macOS 或 WSL 里直接执行前，先确认 `git ls-files -s run.sh reload.sh` 输出 `100755`；若变成 `100644`，用 `git update-index --chmod=+x run.sh reload.sh` 修正后提交；
- 不要用 `chmod +x` 之后不提交的方式“本地修好”，那会在别人克隆时再次丢失。

### 11.3 `.gitignore` 与 `.env.local`

- `.env.local` 必须被忽略，密码只放这里；提交前用 `git status --short` 确认看不到它，再用 `git check-ignore -q .env.local` 的退出码确认（0 = 被忽略，1 = 未被忽略）。注意 `git check-ignore -v` 会把命中的反选规则（以 `!` 开头）也打印出来，不能只看它有没有输出就下结论。
- `vendor/github.com/subosito/gotenv/` 下有 `.env` 和 `.env.invalid` 两个测试夹具，**不能被忽略规则误伤**。`.gitignore` 里对通用的 `.env` / `.env.*` 规则做了显式反选（`!vendor/github.com/subosito/gotenv/.env`、`!vendor/github.com/subosito/gotenv/.env.invalid`），改动忽略规则后必须重新验证这两个文件仍可见。
- `db/gochat.sqlite3` 是上游演示库，需要保留；只忽略运行期旁路文件 `*.sqlite3-journal`、`*.sqlite3-wal`、`*.sqlite3-shm`。
- `bin/gochat.bin` 是编译产物，不提交。

本任务**没有创建** `.env.local`，也没有创建任何含真实 Redis 密码的配置文件；第 4 节描述的是需要加密码时的操作边界。

## 12. 未验证事项

写这份文档时本机没有 Docker、没有 gcc，以下全部**未执行**，不得写成通过：

- [ ] `docker version` 能连上 Linux 引擎。
- [ ] 拉取 `lockgit/gochat:1.18`，记录镜像 ID、RepoDigest 与 `linux/amd64` 架构。
- [ ] 路径 A 的 `docker run` 与 `reload.sh` 实际执行成功。
- [ ] Redis `PING` 返回 `PONG`。
- [ ] `etcdctl endpoint health` 通过，`/gochat_srv` 下能看到 `LogicRpc` / `ConnectRpc` 键。
- [ ] 八个程序 `RUNNING`，uptime 持续增长，日志无反复 `exited` / `FATAL`。
- [ ] `http://127.0.0.1:8080/` 返回 200。
- [ ] `demo / 111111` 登录返回 `code=0` 且 token 非空（演示库是否含该账号也未核实）。
- [ ] `docker port` 与 `Get-NetTCPConnection` 确认宿主机只监听 `127.0.0.1`。
- [ ] 临时副本注册 / 登录冒烟，SQLite 与 Redis 均有实际写入证据。
- [ ] 路径 B `docker build` 成功，记录镜像 ID 与构建日志。
- [ ] 官方镜像不可用时实际改走路径 B，并在部署记录中注明。
- [ ] 加 Redis 密码的完整流程（临时副本渲染 + `redis-cli -a` 验证）。
- [ ] 高并发下 SQLite 的实际表现（本手册只给出静态分析与限制说明）。

已完成的核对都是静态的：启动结构、端口、配置加载路径、SQLite 打开方式与连接池参数、`/health` 端点不存在、`.gitattributes` / `.gitignore` 规则，均按本仓库源码和配置文件逐条对照过；规则本身用临时 git 仓库验证过匹配结果，但没有在真实容器环境里跑过。

# docker --help tree

Captured from local binaries by running only `<command path> --help` (stdin /dev/null, 20s timeout).
Versions: see VERSIONS.txt. Each entry maps to `<path with underscores>.txt`.

Total captured: 419 files, 329299 bytes.

| group | files |
|---|---|
| docker (core CLI) | 131 |
| compose | 37 |
| buildx | 31 |
| plugins | 220 |

Plugins (marked `*` in `docker --help`): agent, ai, buildx, compose, debug, desktop, dhi, extension, init, mcp, model, offload, pass, sandbox, scout

## Failures

None.

## Help resolved to a different command

The Usage line did not name the requested path, so children were not walked (file kept raw):

- `docker build --help` -> `Usage:  docker buildx build [OPTIONS] PATH | URL | -  Start a build`
- `docker bake --help` -> `Usage:  docker buildx bake [OPTIONS] [TARGET...]  Build from a file`
- `docker builder --help` -> `Usage:  docker buildx [OPTIONS] COMMAND  Extended build capabilities with BuildKit`
- `docker image build --help` -> `Usage:  docker buildx build [OPTIONS] PATH | URL | -  Start a build`
- `docker scout attestation --help` -> `Usage  Aliases`
- `docker scout cache --help` -> `Usage  Available Commands`
- `docker scout integration --help` -> `Usage  Available Commands`
- `docker scout repo --help` -> `Usage  Available Commands`
- `docker scout vex --help` -> `Usage  Aliases`
- `docker swarm init --help` -> `Usage:  docker [OPTIONS] COMMAND  docker-init is a Docker CLI plugin`

## Tree

```
docker
  run
  exec
  ps
  build
  bake
  pull
  push
  images
  login
  logout
  search
  version
  info
  agent
    models
      list
    new
    run
    share
      pull
      push
    alias
      add
      list
      remove
    eval
    sandbox
      allow
      deny
      list
    serve
      a2a
      acp
      api
      chat
      mcp
    version
  ai
    version
  builder
  buildx
    dap
      build
    history
      inspect
        attachment
      export
      import
      logs
      ls
      open
      rm
      trace
    imagetools
      create
      inspect
    policy
      eval
      test
    bake
    build
    create
    dial-stdio
    du
    inspect
    ls
    prune
    rm
    stop
    use
    version
  compose
    bridge
      convert
    attach
    build
    commit
    config
    cp
    create
    down
    events
    exec
    export
    images
    kill
    logs
    ls
    pause
    port
    ps
    publish
    pull
    push
    restart
    rm
    run
    scale
    start
    stats
    stop
    top
    unpause
    up
    version
    volumes
    wait
    watch
  container
    attach
    commit
    cp
    create
    diff
    exec
    export
    inspect
    kill
    logs
    ls
    pause
    port
    prune
    rename
    restart
    rm
    run
    start
    stats
    stop
    top
    unpause
    update
    wait
  context
    create
    export
    import
    inspect
    ls
    rm
    show
    update
    use
  debug
  desktop
    disable
    enable
    engine
      ls
      use
    kubernetes
      images
      status
    diagnose
    logs
    restart
    start
    status
    stop
    update
    version
  dhi
    attestation
      get
      list
      sbom
    auth
      apk
      deb
    catalog
      get
      list
    mirror
      list
      start
      stop
  extension
    dev
      debug
      reset
      ui-source
    init
    install
    ls
    rm
    share
    update
    validate
    version
  image
    build
    history
    import
    inspect
    load
    ls
    prune
    pull
    push
    rm
    save
    tag
  init
  manifest
    annotate
    create
    inspect
    push
    rm
  mcp
    catalog
      create
      list
      pull
      push
      remove
      server
        add
        inspect
        ls
        remove
      show
      tag
    client
      connect
      disconnect
      ls
    feature
      disable
      enable
      ls
    gateway
      run
    oauth
      authorize
      ls
      revoke
    profile
      config
      create
      export
      import
      list
      pull
      push
      remove
      server
        add
        ls
        remove
      show
      tools
    secret
      ls
      rm
      set
    server
      init
    template
      list
      use
    tools
      call
      count
      inspect
      ls
    version
  model
    context
      create
      inspect
      ls
      rm
      use
    bench
    df
    gateway
    inspect
    install-runner
    launch
    list
    logs
    package
    ps
    pull
    purge
    push
    requests
    restart-runner
    rm
    run
    search
    show
    skills
    start-runner
    status
    stop-runner
    tag
    unload
    version
  network
    connect
    create
    disconnect
    inspect
    ls
    prune
    rm
  offload
    start
    status
    stop
    version
  pass
    get
    ls
    plugins
      1password
        purge
        setup
      disable
      enable
      ls
    rm
    run
    set
  plugin
    create
    disable
    enable
    inspect
    install
    ls
    push
    rm
    set
    upgrade
  sandbox
    create
      cagent
      claude
      codex
      copilot
      gemini
      kiro
      opencode
      shell
    network
      log
      proxy
    exec
    ls
    reset
    rm
    run
    save
    stop
    version
  scout
    attestation
    cache
    compare
    config
    cves
    enroll
    environment
    integration
    policy
    push
    quickview
    recommendations
    repo
    version
    vex
    watch
  system
    df
    events
    info
    prune
  volume
    create
    inspect
    ls
    prune
    rm
  swarm
    init
    join
  attach
  commit
  cp
  create
  diff
  events
  export
  history
  import
  inspect
  kill
  load
  logs
  pause
  port
  rename
  restart
  rm
  rmi
  save
  start
  stats
  stop
  tag
  top
  unpause
  update
  wait
```

## Gap fill (orchestrator)

The scout subgroups above use an "Available Commands" header the walker missed; their children were captured afterwards with the same `--help`-only rule (12 files):

```
docker scout attestation add | get | list
docker scout cache df | prune
docker scout integration configure | delete | list
docker scout repo disable | enable | list
docker scout vex get
```

Total after gap fill: 431 help captures.

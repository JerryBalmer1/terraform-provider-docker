# The swarm workload layer: docker_secret, docker_config, an overlay network and a docker_service using them, plus a docker_stack with its own network, secret, config and services.

Needs a swarm: `Invoke-Build _Up -Swarm` (manager plus worker4), then
`Invoke-Build _Init, _Plan, _Apply -Scenario swarm_stack`.

After apply, look at:

- `terraform output`: `stack_services` = `{ "manualstack_api" = 2, "manualstack_worker" = 1 }` (names may be
  short), `web_tasks` lists two running tasks.
- `docker -H tcp://127.0.0.1:12375 service ps manual-web`: tasks spread over manager and worker4.
- `docker -H tcp://127.0.0.1:12375 service inspect manual-web`: `Spec.TaskTemplate.ContainerSpec.Secrets`
  (api_key), `Configs` (default.conf), `UpdateConfig` (start-first, rollback), the healthcheck, `EndpointSpec`.
- `docker -H tcp://127.0.0.1:12375 stack ps manualstack` and `stack services manualstack`: the stack objects are
  named `manualstack_<name>` and labelled `com.docker.stack.namespace=manualstack`.
- `docker -H tcp://127.0.0.1:12375 service logs manualstack_api`: the secret and the config, printed by the task.
- A second `_Plan` is empty. Change `replicas` of `manual-web` and `_Plan`: an in-place rolling update.

`_Destroy`, then `_Down`.

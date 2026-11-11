# data.docker_dockerfile -> docker_image build -> docker_container: parse a Dockerfile, build its last stage, run it.

Run: `Invoke-Build _Up`, then `Invoke-Build _Init, _Plan, _Apply -Scenario dockerfile_image_container`.

After apply, look at:

- `terraform output stages` = `["build", "runtime"]`, `built_target` = `runtime`, `image.stages`.
- `terraform output -raw generated_dockerfile` (in `<temp>/tpd-manual/dockerfile_image_container`): the Dockerfile
  rendered from blocks.
- `docker -H tcp://127.0.0.1:12375 exec manual-dockerfile-app cat /srv/build-info.txt`: written by the build stage.
- `docker -H tcp://127.0.0.1:12375 inspect manual-dockerfile-app --format '{{.State.Health.Status}}'`: healthy.
- Edit `test/__manual/dockerfile_image_container/app/Dockerfile` (say, the LABEL) and `_Plan`: the image is
  replaced (its `triggers.dockerfile_sha256` changed) and so is the container that uses it.

`_Destroy`, then `_Down`.

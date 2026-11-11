variable "release_version" {
  type    = string
  default = "1.0.0"
}

resource "terraform_data" "release" {
  input = var.release_version

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_buildx_bake.release]
    }
  }
}

# Builds the "release" group of a bake definition and pushes it. On demand:
#   terraform apply -invoke=action.docker_buildx_bake.release
action "docker_buildx_bake" "release" {
  config {
    working_dir = path.module
    content     = <<-EOT
      variable "VERSION" {
        default = "dev"
      }

      group "release" {
        targets = ["app", "worker"]
      }

      target "_common" {
        args      = { VERSION = VERSION }
        platforms = ["linux/amd64", "linux/arm64"]
      }

      target "app" {
        inherits = ["_common"]
        context  = "app"
        tags     = ["registry.example.com/app:$${VERSION}"]
      }

      # Built first and handed to "worker" as its FROM base (target: context).
      target "base" {
        dockerfile = "base.Dockerfile"
      }

      target "worker" {
        inherits  = ["_common"]
        context   = "worker"
        contexts  = { base = "target:base" }
        tags      = ["registry.example.com/worker:$${VERSION}"]
        # _common's VERSION argument (target.<name>.<attr>). A reference sees
        # the target's own attributes only, not what it inherits, so this
        # names _common rather than app; an unset attribute is null.
        labels    = { "org.opencontainers.image.version" = target._common.args.VERSION }
        resources = { memory = "2g", cpu-quota = 50000 }
      }
    EOT
    targets     = ["release"]
    variables   = { VERSION = var.release_version }
    push        = true
    result_path = "${path.module}/out/bake-result.json"
  }
}

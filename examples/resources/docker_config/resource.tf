resource "docker_config" "nginx" {
  name = "nginx-default.conf"
  data = base64encode(<<-CONF
    server {
      listen 80;
      location / { return 200 "ok\n"; }
    }
  CONF
  )
}

# Terraform 1.11+: write-only content, kept out of plan and state. Bump
# data_wo_version whenever the content changes.
resource "docker_config" "app" {
  name            = "app.ini"
  data_wo         = base64encode("debug = false\n")
  data_wo_version = 1
}

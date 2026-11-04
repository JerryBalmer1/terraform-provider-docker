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

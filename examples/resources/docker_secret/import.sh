# Import by secret name or ID. The daemon never returns secret data, so
# the next plan replaces the secret unless data is ignored or recreated.
terraform import docker_secret.db_password db_password
# Terraform 1.12+ can import by identity instead:
#   import {
#     to       = docker_secret.db_password
#     identity = { id = "<secret ID>" }
#   }

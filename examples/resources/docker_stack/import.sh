# Import by stack name (the com.docker.stack.namespace label value). Secret
# data cannot be read back and volume blocks are not daemon objects, so add
# them to the configuration; the next apply replaces secrets once.
terraform import docker_stack.shop shop

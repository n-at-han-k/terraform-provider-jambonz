# The jambonz Terraform provider, packaged as an image whose only job is to
# carry the binary.
#
# It is never run as a container. provider-opentofu (ns/crossplane-system)
# mounts it as an initContainer and copies the binary into a filesystem mirror
# that OpenTofu resolves the provider from. This provider is not on any
# provider registry, so the mirror is how the workspace gets it.
#
# The mirror layout OpenTofu expects is
#   <mirror>/<host>/<namespace>/<type>/<version>/<os>_<arch>/terraform-provider-<type>_v<version>
# and the initContainer builds that path, so this image only needs the binary
# somewhere predictable.
#
# The build compiles the COMMITTED generated code -- `make gen` is not run here.
# Regeneration needs Node, Terraform and the codegen binaries, and the Tests
# workflow already fails if what is committed and what the spec says disagree.
FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY . /src

# CGO off: the binary is copied into the provider pod and must not depend on
# that image's libc.
RUN CGO_ENABLED=0 go build -trimpath -o /out/terraform-provider-jambonz .

FROM debian:bookworm-slim

COPY --from=build /out/terraform-provider-jambonz /provider/terraform-provider-jambonz

# Non-functional; the image exists to be copied out of.
CMD ["/bin/true"]

# @clover: track=nonroot
FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

ARG TARGETPLATFORM

COPY $TARGETPLATFORM/github-docs-mcp /github-docs-mcp

# Identify the MCP server provided by this image.
LABEL io.modelcontextprotocol.server.name="io.github.matcra587/github-docs-mcp"

# stdio, so that `docker run -i <image>` works with no configuration. That is
# what an MCP client does, and the only transport a registry package may
# declare. Run detached without `-e MCP_TRANSPORT=http` and the server sees EOF
# on stdin and exits.
#
# MCP_HTTP_ADDR stays set so asking for HTTP is a one-liner. Binding all
# interfaces is safe here: reachability is governed by port mapping and
# firewalling, not bind address.
ENV MCP_TRANSPORT=stdio \
    MCP_HTTP_ADDR=0.0.0.0:8080

EXPOSE 8080

# 65532 is distroless's nonroot user, named numerically so a runtime can check
# the uid before start rather than having to resolve it from the image.
USER 65532:65532

ENTRYPOINT ["/github-docs-mcp"]

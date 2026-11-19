# The Reactorcide egress proxy image. Build from this directory:
#   docker build -t <registry>/reactorcide/egress:<tag> egress_proxy
# It holds two programs:
#   /usr/local/bin/reactorcide-egress      the proxy (Rust), the entrypoint
#   /usr/local/bin/reactorcide-egress-net  the job network helper (Go): the
#       Kubernetes init container and sidecar, and the Docker and containerd
#       helper container. The worker sets the command for it.
# No WASM plugins are in the image. An operator mounts plugin files and
# grants them in the policy.
#
# Each base image has an exact version and the digest of its multi-platform
# index, so a rebuild of the same source uses the same bases. To update a
# base, change the tag and the digest together. Get the digest from the
# Docker-Content-Digest header of the registry manifest request.

FROM rust:1.98.1-bookworm@sha256:93ce27a88655056a51dbdd8f5f2d7ddc071c7b0070fb288a37b5a285fc83971e AS proxy
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY wit ./wit
# Cache the dependencies in a separate layer.
RUN mkdir src && echo 'fn main() {}' > src/main.rs && touch src/lib.rs \
 && cargo build --release --locked --bin reactorcide-egress && rm -rf src
COPY src ./src
RUN touch src/main.rs src/lib.rs && cargo build --release --locked --bin reactorcide-egress

FROM golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0 AS net
WORKDIR /src
COPY netd/go.mod netd/go.sum ./
RUN go mod download
COPY netd ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/reactorcide-egress-net ./cmd/reactorcide-egress-net

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f
COPY --from=proxy /src/target/release/reactorcide-egress /usr/local/bin/reactorcide-egress
COPY --from=net /out/reactorcide-egress-net /usr/local/bin/reactorcide-egress-net
USER 65532:65532
ENV REACTORCIDE_EGRESS_LISTEN=:8080 REACTORCIDE_EGRESS_PROXY_LISTEN=:3128
EXPOSE 8080 3128
ENTRYPOINT ["/usr/local/bin/reactorcide-egress"]

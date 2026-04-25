######################################################
## Base Image                                      ##
######################################################
ARG GO_VERSION=1.25

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS builder

# TARGETOS / TARGETARCH are auto-populated by BuildKit per --platform target.
# They MUST be (re-)declared inside the stage to be visible to ENV / RUN —
# global ARGs (before the first FROM) aren't propagated automatically.
ARG TARGETOS
ARG TARGETARCH

ENV CGO_ENABLED=0
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/imagelet ./cmd/imagelet


######################################################
## Final Image                                      ##
######################################################
ARG BUILD_VER

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/imagelet /imagelet

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/imagelet"]

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/crateyard ./cmd/crateyard
COPY VERSION ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(cat VERSION)" -o /crateyard ./cmd/crateyard && mkdir /data

FROM scratch
COPY --from=build /crateyard /crateyard
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/crateyard"]
